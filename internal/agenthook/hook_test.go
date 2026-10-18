package agenthook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/webhook"
)

const (
	inst   = "i1"
	chat   = "5511999999999@s.whatsapp.net"
	secret = "test-secret-0123456789"
)

// fakeMessenger records what the hook asked to send.
type fakeMessenger struct {
	mu      sync.Mutex
	sends   []service.SendInput
	reads   []string
	cancels []string
	sendErr error
}

func (f *fakeMessenger) Send(_ context.Context, _ string, in service.SendInput, _ time.Duration) (service.MessageView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return service.MessageView{}, f.sendErr
	}
	f.sends = append(f.sends, in)
	return service.MessageView{ID: fmt.Sprintf("q%d", len(f.sends)), Status: "queued"}, nil
}

func (f *fakeMessenger) MarkRead(_ context.Context, _ string, c string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, c)
	return nil
}

func (f *fakeMessenger) CancelAgentReplies(_ context.Context, _ string, c, _ string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, c)
	return 1, nil
}

func (f *fakeMessenger) canceled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.cancels...)
}

func (f *fakeMessenger) snapshot() ([]service.SendInput, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]service.SendInput{}, f.sends...), append([]string{}, f.reads...)
}

// recorder collects agent events.
type recorder struct {
	mu  sync.Mutex
	evs []recorded
}

type recorded struct {
	typ  string
	data any
}

func (r *recorder) Publish(_ string, typ string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, recorded{typ, data})
}

func (r *recorder) of(typ string) []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []any
	for _, e := range r.evs {
		if e.typ == typ {
			out = append(out, e.data)
		}
	}
	return out
}

type env struct {
	t     *testing.T
	st    *store.Store
	clk   *clock.Fake
	hook  *Hook
	bus   *events.Bus
	msgr  *fakeMessenger
	rec   *recorder
	srv   *httptest.Server
	calls atomic.Int32
	// handler answers hook requests (default: 204).
	mu      sync.Mutex
	handler func(w http.ResponseWriter, r *http.Request, req Request)
	held    atomic.Bool
	seq     int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateInstance(context.Background(), store.Instance{ID: inst, Name: inst, State: instance.Connected, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, clk: clock.NewFake(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)),
		msgr: &fakeMessenger{}, rec: &recorder{}}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if err := webhook.Verify(secret, r.Header.Get(HeaderSignature), body, e.clk.Now(), 5*time.Minute); err != nil {
			t.Errorf("signature: %v", err)
		}
		if r.Header.Get(HeaderEventType) != RequestEventType || r.Header.Get(HeaderEventID) == "" {
			t.Errorf("headers: %v", r.Header)
		}
		var req Request
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("request body: %v", err)
		}
		e.mu.Lock()
		h := e.handler
		e.mu.Unlock()
		if h == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h(w, r, req)
	}))
	t.Cleanup(e.srv.Close)
	e.hook = New(st, Options{Clock: e.clk, Client: e.srv.Client(), Held: func(string) bool { return e.held.Load() }})
	e.hook.SetMessenger(e.msgr)
	e.hook.SetPublisher(e.rec)
	e.bus = events.NewBus(e.clk, e.hook)
	e.hook.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		e.hook.Close(ctx)
	})
	e.configure(func(c *Config) {})
	return e
}

func (e *env) setHandler(h func(w http.ResponseWriter, r *http.Request, req Request)) {
	e.mu.Lock()
	e.handler = h
	e.mu.Unlock()
}

// configure stores an enabled config pointing at the test server, adjusted by fn.
func (e *env) configure(fn func(*Config)) {
	e.t.Helper()
	c := DefaultConfig()
	c.URL, c.Secret, c.Enabled, c.Timeout = e.srv.URL, secret, true, Duration(time.Second)
	fn(&c)
	b, _ := json.Marshal(c)
	if err := e.st.PutAgentHook(context.Background(), inst, string(b)); err != nil {
		e.t.Fatal(err)
	}
}

// receive logs an incoming message and publishes message.received, as the ingest does.
func (e *env) receive(chatJID, text string) events.Message {
	e.t.Helper()
	e.seq++
	m := events.Message{ID: fmt.Sprintf("msg_%d", e.seq), WAMessageID: fmt.Sprintf("W%d", e.seq), Chat: chatJID,
		Sender: chatJID, Source: "contact", Type: "text", Text: text, Timestamp: e.clk.Now().Add(time.Duration(e.seq) * time.Millisecond)}
	if _, err := e.st.InsertMessage(context.Background(), inst, store.Message{ID: m.ID, WAMessageID: m.WAMessageID,
		ChatJID: chatJID, SenderJID: chatJID, Source: "contact", Type: "text", Text: text, Status: "received",
		Timestamp: m.Timestamp}, store.ChatUpdate{Name: "Ana", Inbound: true}); err != nil {
		e.t.Fatal(err)
	}
	e.bus.Publish(inst, events.TypeMessageReceived, m)
	return m
}

// humanSends publishes a from_me message sent from the phone.
func (e *env) humanSends(chatJID string) {
	e.seq++
	e.bus.Publish(inst, events.TypeMessageSent, events.Message{ID: fmt.Sprintf("msg_%d", e.seq),
		WAMessageID: fmt.Sprintf("W%d", e.seq), Chat: chatJID, FromMe: true, Source: "device", Type: "text", Text: "humano"})
}

func (e *env) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *env) callsLog() []store.AgentCall {
	list, err := e.st.ListAgentCalls(context.Background(), inst, 100)
	if err != nil {
		e.t.Fatal(err)
	}
	return list
}

// settle waits until the hook has no chat goroutine and its buffer is empty.
func (e *env) settle() {
	e.t.Helper()
	e.eventually("hook idle", func() bool {
		e.hook.mu.Lock()
		n := len(e.hook.chats)
		e.hook.mu.Unlock()
		return n == 0 && len(e.hook.in) == 0
	})
	time.Sleep(20 * time.Millisecond) // the router may hold one event already taken from the buffer
	e.eventually("hook idle", func() bool {
		e.hook.mu.Lock()
		defer e.hook.mu.Unlock()
		return len(e.hook.chats) == 0
	})
}

func reply(v any) func(w http.ResponseWriter, r *http.Request, req Request) {
	return func(w http.ResponseWriter, _ *http.Request, _ Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

func TestReplyAndActionsGoThroughMessenger(t *testing.T) {
	e := newEnv(t)
	var got Request
	var gotMu sync.Mutex
	e.setHandler(func(w http.ResponseWriter, r *http.Request, req Request) {
		gotMu.Lock()
		got = req
		gotMu.Unlock()
		reply(map[string]any{"reply": "Olá! Como posso ajudar?",
			"actions": []any{map[string]any{"type": "mark_read"}, map[string]any{"type": "react", "emoji": "👍"}}})(w, r, req)
	})
	e.receive(chat, "primeira")
	e.settle()
	m := e.receive(chat, "preciso de ajuda")
	e.eventually("reply", func() bool { s, _ := e.msgr.snapshot(); return len(s) >= 4 })
	e.settle()

	gotMu.Lock()
	defer gotMu.Unlock()
	if got.InstanceID != inst || got.Chat.JID != chat || got.Chat.Name != "Ana" || got.Chat.IsGroup {
		t.Fatalf("request = %+v", got)
	}
	var data events.Message
	if err := json.Unmarshal(got.Message, &data); err != nil || data.ID != m.ID || data.Text != "preciso de ajuda" {
		t.Fatalf("message = %s", got.Message)
	}
	if len(got.Context) != 2 || got.Context[0].Text != "primeira" || got.Context[1].ID != m.ID {
		t.Fatalf("context = %+v", got.Context)
	}
	sends, reads := e.msgr.snapshot()
	if len(reads) != 2 || reads[0] != chat {
		t.Fatalf("reads = %v", reads)
	}
	// Per call: the reply, then the reaction.
	text, react := sends[2], sends[3]
	if react.Type != "reaction" || react.Reaction.MessageID != m.ID || react.Reaction.Emoji != "👍" || react.To != chat {
		t.Fatalf("react = %+v", react)
	}
	if text.Type != "text" || text.Text != "Olá! Como posso ajudar?" || text.To != chat || text.ClientMessageID != "agent:"+m.ID {
		t.Fatalf("reply = %+v", text)
	}
	log := e.callsLog()
	if len(log) != 2 || log[0].Status != "ok" || !log[0].Replied || log[0].ReplyID == "" || log[0].Actions != "mark_read,react" || log[0].HTTPStatus != 200 {
		t.Fatalf("log = %+v", log)
	}
}

func TestReplyObjectIsForcedToTheChat(t *testing.T) {
	e := newEnv(t)
	e.setHandler(reply(map[string]any{"reply": map[string]any{"type": "location", "to": "5511000000000",
		"client_message_id": "x", "location": map[string]any{"latitude": -23.5, "longitude": -46.6}}}))
	m := e.receive(chat, "onde fica?")
	e.eventually("reply", func() bool { s, _ := e.msgr.snapshot(); return len(s) == 1 })
	s, _ := e.msgr.snapshot()
	if s[0].Type != "location" || s[0].To != chat || s[0].ClientMessageID != "agent:"+m.ID || s[0].Location == nil {
		t.Fatalf("reply = %+v", s[0])
	}
}

func TestFailClosed(t *testing.T) {
	cases := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request, req Request)
		reason  string
	}{
		{"http error", func(w http.ResponseWriter, _ *http.Request, _ Request) { w.WriteHeader(500) }, "http_error"},
		{"redirect", func(w http.ResponseWriter, r *http.Request, _ Request) {
			http.Redirect(w, r, "http://example.com/", http.StatusFound)
		}, "http_error"},
		{"not json", func(w http.ResponseWriter, _ *http.Request, _ Request) { _, _ = io.WriteString(w, "oi") }, "invalid_response"},
		{"bad reply", reply(map[string]any{"reply": 42}), "invalid_response"},
		{"forbidden type", reply(map[string]any{"reply": map[string]any{"type": "revoke", "message_id": "x"}}), "invalid_response"},
		{"bad known action", reply(map[string]any{"reply": "oi", "actions": []any{map[string]any{"type": "pause"}}}), "invalid_response"},
		{"timeout", func(w http.ResponseWriter, r *http.Request, _ Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
		}, "timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.setHandler(tc.handler)
			m := e.receive(chat, "oi")
			e.eventually("agent.failed", func() bool { return len(e.rec.of(events.TypeAgentFailed)) == 1 })
			e.settle()
			f := e.rec.of(events.TypeAgentFailed)[0].(events.AgentFailed)
			if f.Reason != tc.reason || f.Chat != chat || f.MessageID != m.ID || f.WAMessageID != m.WAMessageID {
				t.Fatalf("failed = %+v", f)
			}
			if s, r := e.msgr.snapshot(); len(s) != 0 || len(r) != 0 {
				t.Fatalf("acted on a failed call: %v %v", s, r)
			}
			if log := e.callsLog(); len(log) != 1 || log[0].Status != "failed" || log[0].Reason != tc.reason {
				t.Fatalf("log = %+v", log)
			}
		})
	}
}

func TestEmptyAnswersAndUnknownActions(t *testing.T) {
	e := newEnv(t)
	e.receive(chat, "a") // 204 by default
	e.settle()
	e.setHandler(reply(map[string]any{"actions": []any{map[string]any{"type": "dance"}}}))
	e.receive(chat, "b")
	e.settle()
	e.setHandler(reply(map[string]any{"reply": "   ", "actions": []any{map[string]any{"type": "dance"}, map[string]any{"type": "mark_read"}}}))
	e.receive(chat, "c")
	e.settle()
	sends, reads := e.msgr.snapshot()
	if len(sends) != 0 || len(reads) != 1 {
		t.Fatalf("sends %v reads %v", sends, reads)
	}
	if n := len(e.rec.of(events.TypeAgentFailed)); n != 0 {
		t.Fatalf("%d failures", n)
	}
	for _, c := range e.callsLog() {
		if c.Status != "ok" {
			t.Fatalf("log = %+v", c)
		}
	}
}

func TestEligibilityRules(t *testing.T) {
	e := newEnv(t)
	group := "120363000000000000@g.us"
	expect := func(want int32) {
		t.Helper()
		e.settle()
		if got := e.calls.Load(); got != want {
			t.Fatalf("calls = %d, want %d", got, want)
		}
	}
	e.receive(chat, "1")
	expect(1)
	e.receive("status@broadcast", "story")
	e.receive("123@newsletter", "canal")
	e.receive(group, "grupo")
	expect(1)
	e.configure(func(c *Config) { c.GroupsEnabled = true })
	e.receive(group, "grupo")
	expect(2)
	e.configure(func(c *Config) { c.Ignore = []string{chat} })
	e.receive(chat, "ignorado")
	expect(2)
	// Instance default off: only chats switched on are handled.
	e.configure(func(c *Config) { c.Enabled = false })
	e.receive(chat, "off")
	expect(2)
	on := true
	if err := e.st.SetChatAgent(context.Background(), inst, chat, &on, true); err != nil {
		t.Fatal(err)
	}
	e.receive(chat, "chat on")
	expect(3)
	off := false
	e.configure(func(c *Config) {})
	_ = e.st.SetChatAgent(context.Background(), inst, chat, &off, false)
	e.receive(chat, "chat off")
	expect(3)
	_ = e.st.SetChatAgent(context.Background(), inst, chat, nil, true)
	_ = e.st.PauseChatAgent(context.Background(), inst, chat, e.clk.Now().Add(time.Minute))
	e.receive(chat, "pausado")
	expect(3)
	e.clk.Advance(2 * time.Minute)
	e.receive(chat, "despausado")
	expect(4)
	// No URL: nothing.
	e.configure(func(c *Config) { c.URL, c.Enabled = "", false })
	e.receive(chat, "sem url")
	expect(4)
}

func TestHumanReplyPausesTheChat(t *testing.T) {
	e := newEnv(t)
	e.configure(func(c *Config) { c.HumanPause = Duration(30 * time.Minute) })
	e.humanSends(chat)
	e.settle()
	ca, err := e.st.GetChatAgent(context.Background(), inst, chat)
	if err != nil || !ca.PausedUntil.Equal(e.clk.Now().Add(30*time.Minute)) {
		t.Fatalf("pause = %+v %v", ca, err)
	}
	e.receive(chat, "oi?")
	e.settle()
	if n := e.calls.Load(); n != 0 {
		t.Fatalf("called during human pause: %d", n)
	}
	e.clk.Advance(31 * time.Minute)
	e.receive(chat, "oi de novo")
	e.settle()
	if n := e.calls.Load(); n != 1 {
		t.Fatalf("calls after pause = %d", n)
	}
	if log := e.callsLog(); log[len(log)-1].Status != "paused" || log[len(log)-1].Reason != "human_reply" {
		t.Fatalf("log = %+v", log)
	}
	// Messages sent by the API (source api) never pause.
	e.bus.Publish(inst, events.TypeMessageSent, events.Message{ID: "x", Chat: chat, FromMe: true, Source: "api", Type: "text"})
	e.settle()
	if ca, _ := e.st.GetChatAgent(context.Background(), inst, chat); ca.PausedUntil.After(e.clk.Now()) {
		t.Fatalf("api send paused the chat: %+v", ca)
	}
	// human_pause 0 disables the pause.
	e.configure(func(c *Config) { c.HumanPause = 0 })
	e.humanSends(chat)
	e.settle()
	if ca, _ := e.st.GetChatAgent(context.Background(), inst, chat); ca.PausedUntil.After(e.clk.Now()) {
		t.Fatalf("paused with human_pause 0: %+v", ca)
	}
}

// block makes the server hold requests until release is closed, tracking concurrency.
func block(e *env, answer any) (release func(), maxInflight *atomic.Int32, texts *[]string, mu *sync.Mutex) {
	gate := make(chan struct{})
	var inflight atomic.Int32
	maxInflight = &atomic.Int32{}
	texts, mu = &[]string{}, &sync.Mutex{}
	e.setHandler(func(w http.ResponseWriter, r *http.Request, req Request) {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			m := maxInflight.Load()
			if n <= m || maxInflight.CompareAndSwap(m, n) {
				break
			}
		}
		var data events.Message
		_ = json.Unmarshal(req.Message, &data)
		mu.Lock()
		*texts = append(*texts, data.Text)
		mu.Unlock()
		<-gate
		reply(answer)(w, r, req)
	})
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }, maxInflight, texts, mu
}

func TestOneCallAtATimePerChatCoalesces(t *testing.T) {
	e := newEnv(t)
	release, maxInflight, texts, mu := block(e, map[string]any{"reply": "ok"})
	e.receive(chat, "m1")
	e.eventually("first call", func() bool { return e.calls.Load() == 1 })
	e.receive(chat, "m2")
	e.receive(chat, "m3")
	time.Sleep(50 * time.Millisecond)
	if e.calls.Load() != 1 {
		t.Fatalf("concurrent call in the same chat")
	}
	release()
	e.settle()
	mu.Lock()
	defer mu.Unlock()
	if len(*texts) != 2 || (*texts)[0] != "m1" || (*texts)[1] != "m3" {
		t.Fatalf("calls carried %v", *texts)
	}
	if maxInflight.Load() != 1 {
		t.Fatalf("max in flight = %d", maxInflight.Load())
	}
	if s, _ := e.msgr.snapshot(); len(s) != 2 {
		t.Fatalf("sends = %d", len(s))
	}
}

func TestOtherChatsRunInParallel(t *testing.T) {
	e := newEnv(t)
	release, maxInflight, _, _ := block(e, map[string]any{})
	defer release()
	e.receive(chat, "a")
	e.receive("5511888888888@s.whatsapp.net", "b")
	e.eventually("two chats in flight", func() bool { return maxInflight.Load() == 2 })
	release()
	e.settle()
}

func TestHumanReplyDuringCallDiscardsTheAnswer(t *testing.T) {
	e := newEnv(t)
	release, _, _, _ := block(e, map[string]any{"reply": "atrasado", "actions": []any{map[string]any{"type": "mark_read"}}})
	e.receive(chat, "oi")
	e.eventually("call", func() bool { return e.calls.Load() == 1 })
	e.receive(chat, "queued meanwhile") // dropped by the human reply
	e.humanSends(chat)
	e.eventually("pause", func() bool {
		ca, _ := e.st.GetChatAgent(context.Background(), inst, chat)
		return ca.PausedUntil.After(e.clk.Now())
	})
	release()
	e.settle()
	if s, r := e.msgr.snapshot(); len(s) != 0 || len(r) != 0 {
		t.Fatalf("acted after a human took over: %v %v", s, r)
	}
	if e.calls.Load() != 1 {
		t.Fatalf("pending message was called: %d", e.calls.Load())
	}
	found := false
	for _, c := range e.callsLog() {
		found = found || c.Status == "discarded"
	}
	if !found {
		t.Fatalf("log = %+v", e.callsLog())
	}
}

func TestLoopGuardPausesTheChat(t *testing.T) {
	e := newEnv(t)
	e.configure(func(c *Config) {
		c.MaxRepliesPerWindow, c.ReplyWindow, c.HumanPause = 2, Duration(10*time.Minute), Duration(time.Hour)
	})
	e.setHandler(reply(map[string]any{"reply": "bot"}))
	for i := 0; i < 2; i++ {
		e.receive(chat, "bot diz oi")
		e.settle()
		e.clk.Advance(time.Minute)
	}
	e.receive(chat, "bot diz oi")
	e.settle()
	if n := e.calls.Load(); n != 2 {
		t.Fatalf("calls = %d", n)
	}
	fs := e.rec.of(events.TypeAgentFailed)
	if len(fs) != 1 || fs[0].(events.AgentFailed).Reason != "loop_guard" {
		t.Fatalf("failed = %+v", fs)
	}
	ca, _ := e.st.GetChatAgent(context.Background(), inst, chat)
	if !ca.PausedUntil.Equal(e.clk.Now().Add(time.Hour)) {
		t.Fatalf("pause = %v", ca.PausedUntil)
	}
	// After the window and the pause, replies resume.
	e.clk.Advance(2 * time.Hour)
	e.receive(chat, "humano de verdade")
	e.settle()
	if n := e.calls.Load(); n != 3 {
		t.Fatalf("calls after pause = %d", n)
	}
}

func TestHandoffAndPauseActions(t *testing.T) {
	e := newEnv(t)
	e.setHandler(reply(map[string]any{"reply": "Vou chamar um atendente.",
		"actions": []any{map[string]any{"type": "handoff", "note": "quer falar com humano"}}}))
	m := e.receive(chat, "quero um humano")
	e.eventually("handoff", func() bool { return len(e.rec.of(events.TypeAgentHandoff)) == 1 })
	e.settle()
	ho := e.rec.of(events.TypeAgentHandoff)[0].(events.AgentHandoff)
	if ho.Chat != chat || ho.MessageID != m.ID || ho.Note != "quer falar com humano" {
		t.Fatalf("handoff = %+v", ho)
	}
	if s, _ := e.msgr.snapshot(); len(s) != 1 || s[0].Text != "Vou chamar um atendente." {
		t.Fatalf("reply before handoff missing: %+v", s)
	}
	ca, _ := e.st.GetChatAgent(context.Background(), inst, chat)
	if ca.Enabled == nil || *ca.Enabled {
		t.Fatalf("chat still on: %+v", ca)
	}
	e.receive(chat, "alô?")
	e.settle()
	if e.calls.Load() != 1 {
		t.Fatal("hook called after handoff")
	}

	other := "5511777777777@s.whatsapp.net"
	e.setHandler(reply(map[string]any{"actions": []any{map[string]any{"type": "pause", "minutes": 5}}}))
	e.receive(other, "x")
	e.settle()
	ca, _ = e.st.GetChatAgent(context.Background(), inst, other)
	if !ca.PausedUntil.Equal(e.clk.Now().Add(5 * time.Minute)) {
		t.Fatalf("pause = %+v", ca)
	}
}

func TestSuspendedSendingSkipsTheCall(t *testing.T) {
	e := newEnv(t)
	e.held.Store(true)
	e.receive(chat, "oi")
	e.settle()
	if e.calls.Load() != 0 {
		t.Fatal("hook called while sending is suspended")
	}
	if log := e.callsLog(); len(log) != 1 || log[0].Status != "skipped" || log[0].Reason != "sending_suspended" {
		t.Fatalf("log = %+v", log)
	}
}

func TestSendFailureIsReported(t *testing.T) {
	e := newEnv(t)
	e.msgr.sendErr = apierr.New(403, "sending_suspended", "suspended", false)
	e.setHandler(reply(map[string]any{"reply": "oi"}))
	e.receive(chat, "oi")
	e.eventually("failure", func() bool { return len(e.rec.of(events.TypeAgentFailed)) == 1 })
	f := e.rec.of(events.TypeAgentFailed)[0].(events.AgentFailed)
	if f.Reason != "send_failed" || f.HTTPStatus != 200 {
		t.Fatalf("failed = %+v", f)
	}
}

func TestCloseWithoutStartAndTwice(t *testing.T) {
	h := New(nil, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	h.Close(ctx)
	h.Close(ctx)
	h.Enqueue(events.Envelope{Type: events.TypeMessageReceived}) // after close: ignored
	if !errors.Is(ctx.Err(), nil) && ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
}

func TestHumanReplyCancelsQueuedAgentReplies(t *testing.T) {
	e := newEnv(t)
	e.humanSends(chat)
	e.settle()
	if c := e.msgr.canceled(); len(c) != 1 || c[0] != chat {
		t.Fatalf("cancels = %v", c)
	}
	log := e.callsLog()
	if len(log) != 1 || log[0].Status != "paused" || !strings.Contains(log[0].Detail, "canceled") {
		t.Fatalf("log = %+v", log)
	}
}

func TestPauseOnAPISend(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, o := range []store.Outbound{
		{ID: "q-crm", InstanceID: inst, ClientMessageID: "crm-1", ChatJID: chat, WAMessageID: "WA1", Payload: "{}"},
		{ID: "q-agent", InstanceID: inst, ClientMessageID: "agent:msg_9", ChatJID: chat, WAMessageID: "WA2", Payload: "{}"},
	} {
		if _, _, err := e.st.Enqueue(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	paused := func() bool {
		ca, _ := e.st.GetChatAgent(ctx, inst, chat)
		return ca.PausedUntil.After(e.clk.Now())
	}
	send := func(id string) {
		e.bus.Publish(inst, events.TypeMessageSent, events.Message{ID: id, Chat: chat, FromMe: true, Source: "api", Type: "text"})
		e.settle()
	}
	send("q-crm") // off by default
	if paused() {
		t.Fatal("api send paused without pause_on_api_send")
	}
	e.configure(func(c *Config) { c.PauseOnAPISend = true })
	send("q-agent") // the hook's own reply never pauses
	if paused() {
		t.Fatal("agent reply paused the chat")
	}
	send("q-crm")
	if !paused() {
		t.Fatal("api send by a human did not pause")
	}
	if log := e.callsLog(); log[0].Reason != "api_send" {
		t.Fatalf("log = %+v", log)
	}
}

func TestStaleMessagesAreSkipped(t *testing.T) {
	e := newEnv(t)
	m := events.Message{ID: "old", WAMessageID: "WOLD", Chat: chat, Sender: chat, Source: "contact", Type: "text",
		Text: "de ontem", Timestamp: e.clk.Now().Add(-3 * time.Hour)}
	e.bus.Publish(inst, events.TypeMessageReceived, m)
	e.settle()
	if e.calls.Load() != 0 {
		t.Fatal("stale message called the hook")
	}
	if log := e.callsLog(); len(log) != 1 || log[0].Status != "skipped" || log[0].Reason != "stale" {
		t.Fatalf("log = %+v", log)
	}
	m.ID, m.Type, m.Timestamp = "unk", "unknown", e.clk.Now()
	e.bus.Publish(inst, events.TypeMessageReceived, m)
	e.settle()
	if e.calls.Load() != 0 {
		t.Fatal("unknown message type called the hook")
	}
}

func TestSecondLoopGuardTripHandsOff(t *testing.T) {
	e := newEnv(t)
	e.configure(func(c *Config) {
		c.MaxRepliesPerWindow, c.ReplyWindow, c.HumanPause = 1, Duration(10*time.Minute), Duration(30*time.Minute)
	})
	e.setHandler(reply(map[string]any{"reply": "bot"}))
	round := func() {
		e.receive(chat, "bot")
		e.settle()
		e.clk.Advance(time.Minute)
		e.receive(chat, "bot") // trips
		e.settle()
	}
	round()
	if len(e.rec.of(events.TypeAgentHandoff)) != 0 {
		t.Fatal("first trip handed off")
	}
	e.clk.Advance(time.Hour) // pause over, window over
	round()
	hs := e.rec.of(events.TypeAgentHandoff)
	if len(hs) != 1 || !strings.HasPrefix(hs[0].(events.AgentHandoff).Note, "loop_guard") {
		t.Fatalf("handoffs = %+v", hs)
	}
	ca, _ := e.st.GetChatAgent(context.Background(), inst, chat)
	if ca.Enabled == nil || *ca.Enabled {
		t.Fatalf("chat not turned off: %+v", ca)
	}
	if len(e.msgr.canceled()) != 2 {
		t.Fatalf("queued replies not canceled on trips: %v", e.msgr.canceled())
	}
}

func TestRejectedReplyStillAppliesHandoffAndSkipsReactions(t *testing.T) {
	e := newEnv(t)
	e.msgr.sendErr = apierr.Validation("media.url is required")
	e.setHandler(reply(map[string]any{"reply": map[string]any{"type": "image"},
		"actions": []any{map[string]any{"type": "react", "emoji": "👍"}, map[string]any{"type": "mark_read"},
			map[string]any{"type": "handoff", "note": "x"}}}))
	e.receive(chat, "oi")
	e.eventually("failure", func() bool { return len(e.rec.of(events.TypeAgentFailed)) == 1 })
	e.settle()
	if f := e.rec.of(events.TypeAgentFailed)[0].(events.AgentFailed); f.Reason != "invalid_response" {
		t.Fatalf("failed = %+v", f)
	}
	if _, reads := e.msgr.snapshot(); len(reads) != 0 {
		t.Fatalf("mark_read ran after a rejected reply: %v", reads)
	}
	if len(e.rec.of(events.TypeAgentHandoff)) != 1 {
		t.Fatal("handoff not applied")
	}
	if log := e.callsLog(); log[0].Actions != "handoff" {
		t.Fatalf("log = %+v", log)
	}
}

func TestMentionAllIsRejected(t *testing.T) {
	e := newEnv(t)
	e.setHandler(reply(map[string]any{"reply": map[string]any{"type": "text", "text": "oi", "mentions": []string{"all"}}}))
	e.receive(chat, "oi")
	e.eventually("failure", func() bool { return len(e.rec.of(events.TypeAgentFailed)) == 1 })
	if s, _ := e.msgr.snapshot(); len(s) != 0 {
		t.Fatalf("sent %v", s)
	}
}
