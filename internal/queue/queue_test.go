package queue

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
	"google.golang.org/grpc"
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
	opt := Options{Clock: fc, Rand: func() float64 { return 0.5 }, Defaults: defaults, Log: slog.New(slog.DiscardHandler),
		Resolver: &lookup.Resolver{Store: st, Workers: ws, Clock: fc}}
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
	return e.enqueueExpiring(chat, p, time.Time{})
}

func (e *env) enqueueExpiring(chat string, p Payload, expires time.Time) string {
	e.t.Helper()
	e.seq++
	id := "q" + string(rune('0'+e.seq))
	b, _ := json.Marshal(p)
	if _, _, err := e.st.Enqueue(context.Background(), store.Outbound{ID: id, InstanceID: "i1", ChatJID: chat,
		WAMessageID: "WA-" + id, Payload: string(b), NextAttemptAt: e.clock.Now(), ExpiresAt: expires}); err != nil {
		e.t.Fatal(err)
	}
	e.q.Wake("i1")
	return id
}

func (e *env) connect() {
	e.t.Helper()
	if err := e.st.UpdateInstanceState(context.Background(), "i1", instance.Connected, ""); err != nil {
		e.t.Fatal(err)
	}
	e.q.StateChanged("i1", instance.Connected)
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

func TestOutsideWindowWaitsForNextWindow(t *testing.T) {
	s := fastSettings()
	s.SendWindow = settings.Window{Start: "08:00", End: "20:00"}
	e := newEnvAt(t, time.Date(2026, 10, 17, 21, 0, 0, 0, time.UTC), s, instance.Connected, nil)
	id := e.enqueue(chatA, Payload{Type: "text", Text: "boa noite"})
	e.eventually("waiting for the window", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(11 * time.Hour) // 08:00: still inside the jitter
	time.Sleep(20 * time.Millisecond)
	if len(e.fake.WA.Sent()) != 0 {
		t.Fatal("sent before window start + jitter")
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(150 * time.Second)
	e.eventually("sent at 08:02:30", func() bool { return e.status(id).Status == store.OutSent })
}

// Replies to an active conversation (something received in the last 24h) are not held
// by the send window; cold recipients wait for the next window.
func TestWindowLetsActiveConversationsThrough(t *testing.T) {
	s := fastSettings()
	s.SendWindow = settings.Window{Start: "08:00", End: "20:00"}
	start := time.Date(2026, 10, 17, 20, 5, 0, 0, time.UTC)
	e := newEnvAt(t, start, s, instance.Connected, nil)
	if _, err := e.st.InsertMessage(context.Background(), "i1", store.Message{ID: "in1", WAMessageID: "IN1", ChatJID: chatA,
		Source: "contact", Type: "text", Status: "received", Timestamp: start.Add(-10 * time.Minute)},
		store.ChatUpdate{Inbound: true}); err != nil {
		t.Fatal(err)
	}
	cold := e.enqueue(chatB, Payload{Type: "text", Text: "oi"})
	reply := e.enqueue(chatA, Payload{Type: "text", Text: "respondendo"})
	e.eventually("reply sent at 20:05", func() bool { return e.status(reply).Status == store.OutSent })
	o := e.status(cold)
	if want := time.Date(2026, 10, 18, 8, 2, 30, 0, time.UTC); o.Status != store.OutQueued || !o.NextAttemptAt.Equal(want) || o.Attempts != 0 {
		t.Fatalf("cold recipient = %+v, want due at %v", o, want)
	}
}

func TestGraceAfterConnect(t *testing.T) {
	e := newEnvAt(t, t0, fastSettings(), instance.Reconnecting, func(o *Options) { o.ConnectGrace = 20 * time.Second })
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.connect()
	e.eventually("grace timer", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(29 * time.Second) // grace is 20s + 0.5 x 20s
	time.Sleep(20 * time.Millisecond)
	if len(e.fake.WA.Sent()) != 0 {
		t.Fatal("sent during the grace period")
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(time.Second)
	e.eventually("sent after grace", func() bool { return e.status(id).Status == store.OutSent })
}

func TestExpiredItemFails(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Reconnecting)
	id := e.enqueueExpiring(chatA, Payload{Type: "text", Text: "oi"}, t0.Add(time.Hour))
	e.clock.Advance(2 * time.Hour)
	e.connect()
	e.eventually("expired", func() bool { return e.status(id).Status == store.OutFailed })
	if o := e.status(id); !strings.HasPrefix(o.LastError, "expired:") || len(e.fake.WA.Sent()) != 0 {
		t.Fatalf("expired item = %+v, sent %d", o, len(e.fake.WA.Sent()))
	}
}

func TestRecipientResolution(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	e.fake.WA.SetRegistered("551188887777", "5511988887777@s.whatsapp.net")
	e.fake.WA.SetUnregistered("5511000000000")
	a := e.enqueue("551188887777@s.whatsapp.net", Payload{Type: "text", Text: "um"})
	b := e.enqueue("551188887777@s.whatsapp.net", Payload{Type: "text", Text: "dois"})
	x := e.enqueue("5511000000000@s.whatsapp.net", Payload{Type: "text", Text: "ninguém"})
	e.eventually("resolved sends", func() bool {
		return e.status(a).Status == store.OutSent && e.status(b).Status == store.OutSent && e.status(x).Status == store.OutFailed
	})
	sent := e.fake.WA.Sent()
	if len(sent) != 2 || sent[0].To != "5511988887777@s.whatsapp.net" || sent[1].To != "5511988887777@s.whatsapp.net" || sent[0].MessageId != "WA-q1" {
		t.Fatalf("sent = %v", sent)
	}
	if o := e.status(x); !strings.HasPrefix(o.LastError, "invalid_number:") {
		t.Fatalf("x = %+v", o)
	}
	if e.fake.WA.Checks() != 2 {
		t.Fatalf("lookups = %d, want 2 (the second item reused the cache/chat)", e.fake.WA.Checks())
	}
}

// A LID answer never becomes the chat: the item goes to the phone JID.
func TestResolutionNeverRetargetsToLID(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	e.fake.WA.SetRegistered("5511777766665", "123456789@lid")
	id := e.enqueue("5511777766665@s.whatsapp.net", Payload{Type: "text", Text: "oi"})
	e.eventually("sent", func() bool { return e.status(id).Status == store.OutSent })
	if to := e.fake.WA.Sent()[0].To; to != "5511777766665@s.whatsapp.net" {
		t.Fatalf("sent to %q", to)
	}
}

// A failing lookup only postpones its own item, with growing backoff, and never re-queries
// before the backoff; a known chat's item is not held.
func TestLookupFailureDefersOnlyThatItem(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	ctx := context.Background()
	if _, err := e.st.InsertMessage(ctx, "i1", store.Message{ID: "in1", WAMessageID: "IN1", ChatJID: chatC,
		Source: "contact", Type: "text", Status: "received", Timestamp: t0}, store.ChatUpdate{Inbound: true}); err != nil {
		t.Fatal(err)
	}
	e.fake.WA.SetCheckErr(status.Error(codes.Unknown, "worker_error: usync failed"))
	x := e.enqueue(chatA, Payload{Type: "text", Text: "novo"})
	c := e.enqueue(chatC, Payload{Type: "text", Text: "conhecido"})
	e.eventually("known chat sent", func() bool { return e.status(c).Status == store.OutSent })
	o := e.status(x)
	if o.Status != store.OutQueued || o.LookupFailures != 1 || o.Attempts != 0 || !o.NextAttemptAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("after 1st lookup failure: %+v", o)
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(30 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if e.fake.WA.Checks() != 1 {
		t.Fatalf("looked up again before the backoff: %d", e.fake.WA.Checks())
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(30 * time.Second)
	e.eventually("2nd lookup failure", func() bool { return e.status(x).LookupFailures == 2 })
	if o := e.status(x); !o.NextAttemptAt.Equal(t0.Add(3 * time.Minute)) {
		t.Fatalf("2nd backoff = %v", o.NextAttemptAt)
	}
	e.fake.WA.SetCheckErr(nil)
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(2 * time.Minute)
	e.eventually("sent once the lookup works", func() bool { return e.status(x).Status == store.OutSent })
}

func TestLookupGivesUpAfterFiveFailures(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	e.fake.WA.SetCheckErr(status.Error(codes.Unknown, "worker_error: usync failed"))
	x := e.enqueue(chatA, Payload{Type: "text", Text: "novo"})
	for i := 1; i < maxLookupFailures; i++ {
		e.eventually("deferred", func() bool { return e.status(x).LookupFailures == i && e.clock.Waiters() > 0 })
		e.clock.Advance(lookupBackoff(i))
	}
	e.eventually("failed", func() bool { return e.status(x).Status == store.OutFailed })
	if o := e.status(x); !strings.HasPrefix(o.LastError, "worker_error: recipient lookup failed") || len(e.fake.WA.Sent()) != 0 {
		t.Fatalf("final = %+v", o)
	}
}

// Past today's ramp limit, new numbers are deferred without a lookup: a stream of lookups
// looks like contact scraping.
func TestRampLimitSkipsLookups(t *testing.T) {
	s := fastSettings()
	s.Ramp = settings.Ramp{Enabled: true, StartPerDay: 1, EndPerDay: 1, Days: 1}
	s.SendWindow = settings.Window{Start: "08:00", End: "20:00"}
	e := newEnv(t, s, instance.Connected)
	a := e.enqueue(chatA, Payload{Type: "text", Text: "um"})
	b := e.enqueue(chatB, Payload{Type: "text", Text: "dois"})
	c := e.enqueue(chatC, Payload{Type: "text", Text: "três"})
	e.eventually("a sent, b and c deferred", func() bool {
		return e.status(a).Status == store.OutSent && strings.HasPrefix(e.status(b).LastError, "ramp_limit:") &&
			strings.HasPrefix(e.status(c).LastError, "ramp_limit:")
	})
	if n := e.fake.WA.Checks(); n != 1 {
		t.Fatalf("lookups = %d, want 1 (only the recipient that went out)", n)
	}
}

func TestLookupRateLimitDefersWithoutCountingFailure(t *testing.T) {
	e := newEnvAt(t, t0, fastSettings(), instance.Connected, func(o *Options) { o.Resolver.PerMinute = 1 })
	a := e.enqueue(chatA, Payload{Type: "text", Text: "um"})
	b := e.enqueue(chatB, Payload{Type: "text", Text: "dois"})
	e.eventually("a sent, b deferred", func() bool {
		return e.status(a).Status == store.OutSent && strings.HasPrefix(e.status(b).LastError, "lookup_rate_limited:")
	})
	if o := e.status(b); o.Status != store.OutQueued || o.LookupFailures != 0 || o.Attempts != 0 ||
		!o.NextAttemptAt.Equal(t0.Add(time.Minute)) || e.fake.WA.Checks() != 1 {
		t.Fatalf("b = %+v, lookups %d", o, e.fake.WA.Checks())
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(time.Minute)
	e.eventually("b sent after a minute", func() bool { return e.status(b).Status == store.OutSent })
}

// blockingCheck holds CheckNumbers until the caller's context ends.
type blockingCheck struct {
	workerv1.WorkerClient
	started chan struct{}
}

func (b blockingCheck) CheckNumbers(ctx context.Context, _ *workerv1.CheckNumbersRequest, _ ...grpc.CallOption) (*workerv1.CheckNumbersResponse, error) {
	close(b.started)
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

// A lookup cut short by shutdown is not a lookup failure (not even the fifth one).
func TestLookupInterruptedByShutdownIsNotAFailure(t *testing.T) {
	started := make(chan struct{})
	e := newEnvAt(t, t0, fastSettings(), instance.Reconnecting, func(o *Options) {
		base, _ := o.Resolver.Workers.Client("i1")
		o.Resolver.Workers = workers{"i1": blockingCheck{WorkerClient: base, started: started}}
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	if err := e.st.DeferLookup(context.Background(), id, t0, maxLookupFailures-1, "worker_error: recipient lookup failed"); err != nil {
		t.Fatal(err)
	}
	e.connect()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("lookup never started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e.q.Shutdown(ctx)
	if o := e.status(id); o.Status != store.OutQueued || o.LookupFailures != maxLookupFailures-1 {
		t.Fatalf("after shutdown: %+v", o)
	}
}

// The stored state may read connected before the connected notice arrives: nothing goes
// out until the notice starts the grace period.
func TestGraceStartsWithTheConnectedNotice(t *testing.T) {
	e := newEnvAt(t, t0, fastSettings(), instance.Reconnecting, func(o *Options) { o.ConnectGrace = 20 * time.Second })
	e.q.StateChanged("i1", instance.Reconnecting)
	if err := e.st.UpdateInstanceState(context.Background(), "i1", instance.Connected, ""); err != nil {
		t.Fatal(err)
	}
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"}) // wakes the dispatcher before the notice
	time.Sleep(50 * time.Millisecond)
	if n := len(e.fake.WA.Sent()); n != 0 {
		t.Fatalf("sent %d before the connected notice", n)
	}
	e.q.StateChanged("i1", instance.Connected)
	e.eventually("grace timer", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(30 * time.Second)
	e.eventually("sent after grace", func() bool { return e.status(id).Status == store.OutSent })
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

// An identical text to new recipients in a row doubles the interval, ramp or not.
func TestDoubledIntervalForIdenticalTextToNewRecipients(t *testing.T) {
	for _, ramp := range []bool{false, true} {
		t.Run(map[bool]string{false: "ramp off", true: "ramp on"}[ramp], func(t *testing.T) {
			s := fastSettings()
			s.MinIntervalMS = 3000
			s.Ramp = settings.Ramp{Enabled: ramp, StartPerDay: 10, EndPerDay: 10, Days: 1}
			e := newEnv(t, s, instance.Connected)
			e.enqueue(chatA, Payload{Type: "text", Text: "promo"})
			e.enqueue(chatB, Payload{Type: "text", Text: "promo"})
			e.eventually("first send", func() bool { return len(e.fake.WA.Sent()) == 1 && e.clock.Waiters() > 0 })
			e.clock.Advance(3 * time.Second)
			time.Sleep(20 * time.Millisecond)
			if n := len(e.fake.WA.Sent()); n != 1 {
				t.Fatalf("sent %d after 3s, want the doubled 6s interval", n)
			}
			e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
			e.clock.Advance(3 * time.Second)
			e.eventually("second send", func() bool { return len(e.fake.WA.Sent()) == 2 })
		})
	}
}

func TestMaxPerMinuteAcrossSends(t *testing.T) {
	s := fastSettings()
	s.MaxPerMinute = 2
	e := newEnv(t, s, instance.Connected)
	e.enqueue(chatA, Payload{Type: "text", Text: "um"})
	e.enqueue(chatB, Payload{Type: "text", Text: "dois"})
	c := e.enqueue(chatC, Payload{Type: "text", Text: "três"})
	e.eventually("two sends", func() bool { return len(e.fake.WA.Sent()) == 2 && e.clock.Waiters() > 0 })
	e.clock.Advance(59 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if n := len(e.fake.WA.Sent()); n != 2 {
		t.Fatalf("sent %d inside the minute, max_per_minute is 2", n)
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(time.Second)
	e.eventually("third send after a minute", func() bool { return e.status(c).Status == store.OutSent })
}

// A retry waits for its backoff and for min_interval since the previous attempt.
func TestPacingAppliesBetweenRetries(t *testing.T) {
	s := fastSettings()
	s.MinIntervalMS = 10000
	e := newEnv(t, s, instance.Connected)
	calls := 0
	e.fake.WA.SetSendFunc(func(req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
		if calls++; calls == 1 {
			return nil, status.Error(codes.Unknown, "worker_error: transient")
		}
		return &workerv1.SendResponse{MessageId: req.MessageId}, nil
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.eventually("first attempt", func() bool { return len(e.fake.WA.Sent()) == 1 && e.clock.Waiters() > 0 })
	e.clock.Advance(retryDelay(1)) // 5s: the backoff is over, min_interval (10s) is not
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(4 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if n := len(e.fake.WA.Sent()); n != 1 {
		t.Fatalf("retried after 9s, min_interval is 10s (%d attempts)", n)
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(time.Second)
	e.eventually("sent at 10s", func() bool { return e.status(id).Status == store.OutSent })
}

// Forget (instance deleted) ends its dispatcher, releases Wait calls on its items and
// drops its per-instance state.
func TestForgetStopsTheDispatcher(t *testing.T) {
	e := newEnvAt(t, t0, fastSettings(), instance.Reconnecting, func(o *Options) { o.ConnectGrace = 20 * time.Second })
	ctx := context.Background()
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.q.StateChanged("i1", instance.Reconnecting) // records readyAt
	e.q.Wake("i1")
	e.eventually("dispatcher running", func() bool { return e.q.Dispatchers() == 1 })
	waited := make(chan error, 1)
	go func() { _, err := e.q.Wait(ctx, "i1", id); waited <- err }()
	e.eventually("waiter registered", func() bool {
		e.q.mu.Lock()
		defer e.q.mu.Unlock()
		return len(e.q.waiters) == 1
	})
	if err := e.st.DeleteInstance(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	e.q.Forget("i1")
	e.eventually("dispatcher gone", func() bool { return e.q.Dispatchers() == 0 })
	select {
	case err := <-waited:
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("wait = %v, want ErrNotFound", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait still blocked after Forget")
	}
	e.q.mu.Lock()
	defer e.q.mu.Unlock()
	if _, ok := e.q.readyAt["i1"]; ok || len(e.q.waiters) != 0 {
		t.Fatalf("state left: readyAt=%v waiters=%v", e.q.readyAt, e.q.waiters)
	}
}
