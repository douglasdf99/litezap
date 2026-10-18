package agenthook

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/ingest"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
)

// TestEndToEndReplyGoesThroughTheQueue wires the real ingest, bus, queue and send path
// on a fake worker: an incoming message reaches the hook, its reply is queued, paced
// (min_interval) and sent by the worker; a from_me message from the phone then pauses
// the chat.
func TestEndToEndReplyGoesThroughTheQueue(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.DiscardHandler)

	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		var m events.Message
		_ = json.Unmarshal(req.Message, &m)
		_ = json.NewEncoder(w).Encode(map[string]any{"reply": "resposta para: " + m.Text})
	}))
	t.Cleanup(agent.Close)

	hook := New(st, Options{Client: agent.Client(), Log: log})
	bus := events.NewBus(nil, hook)
	hook.SetPublisher(bus)

	sp := workertest.NewSpawner()
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	mopt := manager.DefaultOptions()
	mopt.HeartbeatInterval = time.Hour
	mgr := manager.New(st, sp, mopt, log)
	resolver := &lookup.Resolver{Store: st, Workers: mgr}
	set := settings.Defaults()
	set.MinIntervalMS, set.JitterMS, set.TypingSimulation, set.Ramp.Enabled = 1000, 0, false, false
	set.SendWindow = settings.Window{}
	q := queue.New(st, mgr, queue.Options{Defaults: set, Log: log, Resolver: resolver, Events: bus})
	mgr.SetObserver(manager.Observers{&ingest.Ingestor{Store: st, Log: log, Events: bus}, q})
	msg := &service.Messaging{Store: st, Manager: mgr, Queues: q, Resolver: resolver, DataDir: dir, Defaults: set,
		Fetcher: &media.Fetcher{}, Signer: media.NewSigner([]byte("0123456789abcdef0123456789abcdef"), "http://127.0.0.1:1")}
	hook.SetMessenger(service.GuardedMessaging{Messaging: msg, Sending: &service.Sending{Store: st}})
	hook.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		hook.Close(ctx)
		q.Shutdown(ctx)
		mgr.Shutdown(ctx)
	})
	ctx := context.Background()
	if err := st.CreateInstance(ctx, store.Instance{ID: inst, Name: inst, State: instance.Created, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Start(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Admin{Store: st}).Put(ctx, inst, Patch{URL: ptr(agent.URL), Enabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	wait := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait("connected", func() bool { in, _ := st.GetInstance(ctx, inst); return in.State == instance.Connected })
	time.Sleep(50 * time.Millisecond)
	fake := sp.Last(inst)
	for i, text := range []string{"oi", "tudo bem?"} {
		fake.EmitMessage(&workerv1.Message{Id: "IN" + string(rune('A'+i)), Chat: chat, Sender: chat, Type: "text",
			Text: text, PushName: "Ana", TimestampMs: time.Now().UnixMilli()})
		wait("reply sent", func() bool { return len(fake.WA.Sent()) == i+1 })
	}
	sent := fake.WA.Sent()
	if !strings.Contains(sent[0].String(), "resposta para: oi") || !strings.Contains(sent[1].String(), "resposta para: tudo bem?") {
		t.Fatalf("sent = %v", sent)
	}
	// Both replies are API sends in the log (source api), not human messages.
	page, err := msg.ChatMessages(ctx, inst, chat, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	apiReplies := 0
	for _, m := range page.Messages {
		if m.FromMe && m.Source == "api" {
			apiReplies++
		}
	}
	if apiReplies != 2 {
		t.Fatalf("log = %+v", page.Messages)
	}
	if ca, _ := st.GetChatAgent(ctx, inst, chat); ca.PausedUntil.After(time.Now()) {
		t.Fatalf("own replies paused the chat: %+v", ca)
	}
	// The connection drops: the next reply waits in the queue.
	fake.EmitConn("reconnecting", "test")
	wait("reconnecting", func() bool { in, _ := st.GetInstance(ctx, inst); return in.State == instance.Reconnecting })
	fake.EmitMessage(&workerv1.Message{Id: "INC", Chat: chat, Sender: chat, Type: "text", Text: "alguém?",
		PushName: "Ana", TimestampMs: time.Now().UnixMilli()})
	var queued []string
	wait("reply queued", func() bool { queued, _ = st.QueuedAgentItems(ctx, inst, chat); return len(queued) == 1 })
	// A human answers from the phone: the hook pauses in the chat and the queued reply is
	// canceled, so it never lands after the human's message.
	fake.EmitMessage(&workerv1.Message{Id: "PHONE1", Chat: chat, Sender: "5511000000000@s.whatsapp.net", FromMe: true,
		Type: "text", Text: "deixa comigo", TimestampMs: time.Now().UnixMilli()})
	wait("human pause", func() bool {
		ca, _ := st.GetChatAgent(ctx, inst, chat)
		return ca.PausedUntil.After(time.Now().Add(29 * time.Minute))
	})
	wait("queued reply canceled", func() bool {
		o, _ := st.GetOutbound(ctx, inst, queued[0])
		return o.Status == store.OutFailed && queue.Canceled(o.LastError)
	})
	fake.Emit("connected")
	time.Sleep(300 * time.Millisecond)
	if n := len(fake.WA.Sent()); n != 2 {
		t.Fatalf("sent %d messages after the human took over", n)
	}
}
