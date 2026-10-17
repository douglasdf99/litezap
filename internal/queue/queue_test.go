package queue

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type workers map[string]workerv1.WorkerClient

func (w workers) Client(id string) (workerv1.WorkerClient, error) {
	if c, ok := w[id]; ok {
		return c, nil
	}
	return nil, manager.ErrNotRunning
}

type env struct {
	t     *testing.T
	st    *store.Store
	clock *clock.Fake
	q     *Queues
	fake  *workertest.Fake
	seq   int
}

// fastSettings sends back to back: no interval, jitter, typing, ramp or send window.
func fastSettings() settings.Settings {
	s := settings.Defaults()
	s.MinIntervalMS, s.JitterMS, s.TypingSimulation, s.Ramp.Enabled = 0, 0, false, false
	s.SendWindow, s.Timezone, s.UnsafePacing = settings.Window{}, "UTC", true
	return s
}

func newEnv(t *testing.T, defaults settings.Settings, state instance.State) *env {
	t.Helper()
	return newEnvAt(t, t0, defaults, state, nil)
}

// newEnvAt starts the fake clock at start; tweak adjusts the queue options.
func newEnvAt(t *testing.T, start time.Time, defaults settings.Settings, state instance.State, tweak func(*Options)) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateInstance(context.Background(), store.Instance{ID: "i1", Name: "i1", State: state, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	sp := workertest.NewSpawner()
	h, err := sp.Start(context.Background(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	fc := clock.NewFake(start)
	ws := workers{"i1": h.Client()}
	opt := Options{Clock: fc, Rand: func() float64 { return 0.5 }, Defaults: defaults, Log: slog.New(slog.DiscardHandler)}
	if tweak != nil {
		tweak(&opt)
	}
	q := New(st, ws, opt)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		q.Shutdown(ctx)
	})
	return &env{t: t, st: st, clock: fc, q: q, fake: sp.Last("i1")}
}

func (e *env) enqueue(chat string, p Payload) string {
	e.t.Helper()
	e.seq++
	id := "q" + string(rune('0'+e.seq))
	b, _ := json.Marshal(p)
	if _, _, err := e.st.Enqueue(context.Background(), store.Outbound{ID: id, InstanceID: "i1", ChatJID: chat,
		WAMessageID: "WA-" + id, Payload: string(b), NextAttemptAt: e.clock.Now()}); err != nil {
		e.t.Fatal(err)
	}
	e.q.Wake("i1")
	return id
}

func (e *env) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (e *env) status(id string) store.Outbound {
	e.t.Helper()
	o, err := e.st.GetOutbound(context.Background(), "i1", id)
	if err != nil {
		e.t.Fatal(err)
	}
	return o
}

func (e *env) sentTo() []string {
	var out []string
	for _, r := range e.fake.WA.Sent() {
		out = append(out, r.MessageId)
	}
	return out
}

const (
	chatA = "5511900000001@s.whatsapp.net"
	chatB = "5511900000002@s.whatsapp.net"
	chatC = "5511900000003@s.whatsapp.net"
)

func TestSendsInOrderAndLogs(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	a1 := e.enqueue(chatA, Payload{Type: "text", Text: "um"})
	e.enqueue(chatA, Payload{Type: "text", Text: "dois"})
	e.enqueue(chatB, Payload{Type: "text", Text: "três"})
	e.eventually("3 sends", func() bool { return len(e.fake.WA.Sent()) == 3 })
	got := e.sentTo()
	if got[0] != "WA-q1" || got[1] != "WA-q2" || got[2] != "WA-q3" {
		t.Fatalf("send order = %v", got)
	}
	e.eventually("a1 sent", func() bool { return e.status(a1).Status == store.OutSent })
	m, err := e.st.GetMessage(context.Background(), "i1", a1)
	if err != nil || m.Source != "api" || m.Text != "um" || m.WAMessageID != "WA-q1" {
		t.Fatalf("logged = %+v, %v", m, err)
	}
}

func TestPausedWhileNotConnected(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Reconnecting)
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	time.Sleep(50 * time.Millisecond)
	if n := len(e.fake.WA.Sent()); n != 0 {
		t.Fatalf("sent %d while reconnecting", n)
	}
	if err := e.st.UpdateInstanceState(context.Background(), "i1", instance.Connected, ""); err != nil {
		t.Fatal(err)
	}
	e.q.StateChanged("i1", instance.Connected)
	e.eventually("sent after connect", func() bool { return e.status(id).Status == store.OutSent })
	if a := e.status(id).Attempts; a != 0 {
		t.Fatalf("attempts = %d", a)
	}
}

func TestRetriesThenFails(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	e.fake.WA.SetSendFunc(func(*workerv1.SendRequest) (*workerv1.SendResponse, error) {
		return nil, status.Error(codes.Unknown, "worker_error: server returned error 500")
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	for i := 1; i < 5; i++ {
		e.eventually("attempt", func() bool { return len(e.fake.WA.Sent()) == i && e.clock.Waiters() > 0 })
		if o := e.status(id); o.Status != store.OutQueued || o.Attempts != i {
			t.Fatalf("after attempt %d: %+v", i, o)
		}
		e.clock.Advance(retryDelay(i))
	}
	e.eventually("failed", func() bool { return e.status(id).Status == store.OutFailed })
	if o := e.status(id); o.Attempts != 5 || o.LastError != "worker_error: server returned error 500" {
		t.Fatalf("final = %+v", o)
	}
}

func TestPermanentFailureAndWait(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	e.fake.WA.SetSendFunc(func(*workerv1.SendRequest) (*workerv1.SendResponse, error) {
		return nil, status.Error(codes.InvalidArgument, "invalid_number: recipient is not on WhatsApp")
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	o, err := e.q.Wait(ctx, "i1", id)
	if err != nil || o.Status != store.OutFailed || o.Attempts != 1 || o.LastError != "invalid_number: recipient is not on WhatsApp" {
		t.Fatalf("wait = %+v, %v", o, err)
	}
}

func TestDisconnectDuringSendKeepsAttempts(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	e.fake.WA.SetSendFunc(func(*workerv1.SendRequest) (*workerv1.SendResponse, error) {
		// The connection drops mid-send: the manager records reconnecting first.
		_ = e.st.UpdateInstanceState(context.Background(), "i1", instance.Reconnecting, "disconnected")
		return nil, status.Error(codes.Unavailable, "instance_not_connected: websocket not connected")
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.eventually("requeued", func() bool {
		o := e.status(id)
		return len(e.fake.WA.Sent()) == 1 && o.Status == store.OutQueued
	})
	if o := e.status(id); o.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0", o.Attempts)
	}
}

func TestPacingAndTyping(t *testing.T) {
	s := fastSettings()
	s.MinIntervalMS, s.TypingSimulation = 3000, true
	e := newEnv(t, s, instance.Connected)
	e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.enqueue(chatB, Payload{Type: "text", Text: "olá"})
	// Typing first: "composing", then 2 chars x 50ms.
	e.eventually("typing", func() bool { return len(e.fake.WA.Presences()) == 1 && e.clock.Waiters() > 0 })
	if p := e.fake.WA.Presences()[0]; p.State != "composing" || p.To != chatA {
		t.Fatalf("presence = %+v", p)
	}
	e.clock.Advance(100 * time.Millisecond)
	e.eventually("first send", func() bool { return len(e.fake.WA.Sent()) == 1 && e.clock.Waiters() > 0 })
	e.clock.Advance(2 * time.Second) // still inside the 3s interval
	time.Sleep(20 * time.Millisecond)
	if len(e.fake.WA.Presences()) != 1 {
		t.Fatal("second message started before min_interval")
	}
	e.clock.Advance(time.Second)
	e.eventually("second typing", func() bool { return len(e.fake.WA.Presences()) == 2 && e.clock.Waiters() > 0 })
	e.clock.Advance(150 * time.Millisecond)
	e.eventually("second send", func() bool { return len(e.fake.WA.Sent()) == 2 })
}

func TestRampDefersNewRecipientsToNextWindow(t *testing.T) {
	s := fastSettings()
	s.Ramp = settings.Ramp{Enabled: true, StartPerDay: 1, EndPerDay: 1, Days: 1}
	s.SendWindow = settings.Window{Start: "08:00", End: "20:00"}
	e := newEnv(t, s, instance.Connected)
	ctx := context.Background()
	// chatC already wrote to us: replies never count against the ramp.
	if _, err := e.st.InsertMessage(ctx, "i1", store.Message{ID: "in1", WAMessageID: "IN1", ChatJID: chatC,
		Source: "contact", Type: "text", Status: "received", Timestamp: t0}, store.ChatUpdate{Inbound: true}); err != nil {
		t.Fatal(err)
	}
	g := e.enqueue("120363000000000001@g.us", Payload{Type: "text", Text: "oi grupo"}) // a group with no inbound counts
	a := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	c := e.enqueue(chatC, Payload{Type: "text", Text: "oi"})
	g2 := e.enqueue("120363000000000001@g.us", Payload{Type: "text", Text: "de novo"}) // same recipient: counted once
	e.eventually("group, c and g2 sent", func() bool {
		return e.status(g).Status == store.OutSent && e.status(c).Status == store.OutSent && e.status(g2).Status == store.OutSent
	})
	o := e.status(a)
	// Next day's window start (08:00) plus jitter (0.5 x 5min), not midnight.
	want := time.Date(2026, 10, 18, 8, 2, 30, 0, time.UTC)
	if o.Status != store.OutQueued || o.Attempts != 0 || !o.NextAttemptAt.Equal(want) || o.LastError == "" {
		t.Fatalf("a = %+v, want due at %v", o, want)
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(12 * time.Hour) // midnight: nothing happens
	time.Sleep(20 * time.Millisecond)
	if e.status(a).Status != store.OutQueued {
		t.Fatal("deferred item sent at midnight")
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(8*time.Hour + 150*time.Second)
	e.eventually("a sent in the next window", func() bool { return e.status(a).Status == store.OutSent })
}

func TestWorkerNotConnectedKeepsAttempts(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	calls := 0
	e.fake.WA.SetSendFunc(func(req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
		if calls++; calls == 1 { // the stored state still says connected
			return nil, status.Error(codes.Unknown, "instance_not_connected: websocket not connected")
		}
		return &workerv1.SendResponse{MessageId: req.MessageId}, nil
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.eventually("requeued", func() bool { return len(e.fake.WA.Sent()) == 1 && e.clock.Waiters() > 0 })
	if o := e.status(id); o.Status != store.OutQueued || o.Attempts != 0 {
		t.Fatalf("after worker said not connected: %+v", o)
	}
	e.clock.Advance(errPause)
	e.eventually("sent", func() bool { return e.status(id).Status == store.OutSent })
}

// A delivery receipt that beat a failed Send answer proves the message went out.
func TestReceiptBeatsFailedAnswer(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	e.fake.WA.SetSendFunc(func(req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
		_ = e.st.ApplyReceipt(context.Background(), "i1", []string{req.MessageId}, "delivered")
		return nil, status.Error(codes.Unknown, "worker_error: answer lost")
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.eventually("sent", func() bool { return e.status(id).Status == store.OutSent })
	if o := e.status(id); o.Attempts != 0 || len(e.fake.WA.Sent()) != 1 {
		t.Fatalf("item = %+v, sends %d", o, len(e.fake.WA.Sent()))
	}
	if m, _ := e.st.GetMessage(context.Background(), "i1", id); m.Status != "delivered" {
		t.Fatalf("log status = %q", m.Status)
	}
}

func TestShutdownDuringTypingRequeues(t *testing.T) {
	s := fastSettings()
	s.TypingSimulation = true
	e := newEnv(t, s, instance.Connected)
	id := e.enqueue(chatA, Payload{Type: "text", Text: "uma mensagem longa"})
	e.eventually("typing", func() bool { return len(e.fake.WA.Presences()) == 1 && e.clock.Waiters() > 0 })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e.q.Shutdown(ctx)
	if o := e.status(id); o.Status != store.OutQueued || o.Attempts != 0 || len(e.fake.WA.Sent()) != 0 {
		t.Fatalf("after shutdown: %+v, sent %d", o, len(e.fake.WA.Sent()))
	}
}

func TestMediaFileIsSentAndRemoved(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	f := filepath.Join(t.TempDir(), "img")
	if err := os.WriteFile(f, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := e.enqueue(chatA, Payload{Type: "image", Text: "legenda", MediaFile: f, Mime: "image/png", Size: 9})
	e.eventually("sent", func() bool { return e.status(id).Status == store.OutSent })
	req := e.fake.WA.Sent()[0]
	if req.MediaPath != f || string(e.fake.WA.SentMedia(req.MessageId)) != "png-bytes" || req.Mime != "image/png" || req.Text != "legenda" {
		t.Fatalf("request = %+v", req)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatalf("staged file not removed: %v", err)
	}
	m, _ := e.st.GetMessage(context.Background(), "i1", id)
	if m.MediaJSON == "" {
		t.Fatal("sent media not described in the log")
	}
}

func TestDeletedInstanceStopsDispatcher(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	if err := e.st.DeleteInstance(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	e.q.Wake("i1")
	e.eventually("dispatcher gone", func() bool {
		e.q.mu.Lock()
		defer e.q.mu.Unlock()
		return len(e.q.disp) == 0
	})
}
