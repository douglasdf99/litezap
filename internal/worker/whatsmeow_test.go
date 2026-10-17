package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type rec struct {
	mu    sync.Mutex
	codes []string
	tos   []time.Duration
}

func (r *rec) ConnState(string, string, string) {}
func (r *rec) Data(*workerv1.Event)             {}
func (r *rec) QR(code string, to time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.codes = append(r.codes, code)
	r.tos = append(r.tos, to)
}
func (r *rec) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.codes...)
}

var fast = qrTiming{First: 60 * time.Millisecond, Next: 20 * time.Millisecond}

func TestRotateQRsExhaustsCodes(t *testing.T) {
	r := &rec{}
	firsts := 0
	ok := rotateQRs(context.Background(), []string{"a", "b", "c"}, fast, r, nil, func() { firsts++ })
	if !ok || firsts != 1 {
		t.Fatalf("ok=%v firsts=%d", ok, firsts)
	}
	if got := r.snapshot(); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("codes = %v", got)
	}
	if r.tos[0] != fast.First || r.tos[1] != fast.Next || r.tos[2] != fast.Next {
		t.Fatalf("timeouts = %v", r.tos)
	}
}

func TestRotateQRsCancel(t *testing.T) {
	r := &rec{}
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan bool, 1)
	go func() {
		res <- rotateQRs(ctx, []string{"a", "b"}, qrTiming{First: time.Hour, Next: time.Hour}, r, nil, func() {})
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case ok := <-res:
		if ok {
			t.Fatal("cancelled rotation reported exhaustion")
		}
	case <-time.After(time.Second):
		t.Fatal("rotation did not stop")
	}
}

func TestRotateQRsRotatesSecret(t *testing.T) {
	r := &rec{}
	rot := make(chan *events.RotateADVSecret, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		rot <- &events.RotateADVSecret{OldSecret: "OLD", NewSecret: "NEW"}
	}()
	rotateQRs(context.Background(), []string{"xOLD", "yOLD"}, fast, r, rot, func() {})
	got := r.snapshot()
	if len(got) < 3 || got[0] != "xOLD" || got[1] != "xNEW" || got[len(got)-1] != "yNEW" {
		t.Fatalf("codes = %v", got)
	}
}

type connEvt struct{ state, reason string }

type stateRec struct {
	mu     sync.Mutex
	states []connEvt
	qrs    []string
}

func (r *stateRec) ConnState(state, reason, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, connEvt{state, reason})
}
func (r *stateRec) Data(*workerv1.Event) {}
func (r *stateRec) QR(code string, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.qrs = append(r.qrs, code)
}
func (r *stateRec) snapshot() ([]connEvt, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]connEvt(nil), r.states...), append([]string(nil), r.qrs...)
}

// newTestAdapter builds an adapter over a real (never connected) whatsmeow client whose
// connect is replaced by fake.
func newTestAdapter(t *testing.T, loggedIn bool, fake func(context.Context) error) (*Whatsmeow, *stateRec) {
	t.Helper()
	dev := &store.Device{}
	if loggedIn {
		jid := types.NewJID("5511999999999", types.DefaultUserServer)
		dev.ID = &jid
	}
	w := NewWhatsmeow(whatsmeow.NewClient(dev, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.connect = fake
	w.failureBase = 10 * time.Millisecond
	r := &stateRec{}
	w.sink = r
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w.loopCtx, w.cancel = ctx, cancel
	return w, r
}

func TestTemporaryBanIsRecoverableFailure(t *testing.T) {
	w, r := newTestAdapter(t, true, func(context.Context) error { return nil })
	w.handle(&events.TemporaryBan{Code: events.TempBanReason(101), Expire: time.Hour})
	states, _ := r.snapshot()
	want := connEvt{"failed", "temp_banned:1h0m0s"}
	if len(states) != 1 || states[0] != want {
		t.Fatalf("states = %v, want [%v]", states, want)
	}
	if !w.terminal.Load() {
		t.Fatal("temporary ban must stop the adapter's loops")
	}
}

func TestConnectFailureWhileLoggedInReconnects(t *testing.T) {
	var calls atomic.Int32
	block := make(chan struct{})
	w, r := newTestAdapter(t, true, func(ctx context.Context) error {
		calls.Add(1)
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil
	})
	defer close(block)
	// Two failures in a row must not start two connect loops.
	w.handle(&events.ConnectFailure{Reason: events.ConnectFailureReason(418)})
	w.handle(&events.ConnectFailure{Reason: events.ConnectFailureReason(418)})
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("connect calls = %d, want 1", n)
	}
	states, _ := r.snapshot()
	if len(states) == 0 || states[0] != (connEvt{"reconnecting", "connect_failure_418"}) {
		t.Fatalf("states = %v", states)
	}
}

func TestConnectFailureAfterTerminalDoesNotReconnect(t *testing.T) {
	var calls atomic.Int32
	w, _ := newTestAdapter(t, true, func(context.Context) error { calls.Add(1); return nil })
	w.terminal.Store(true)
	w.handle(&events.ConnectFailure{Reason: events.ConnectFailureReason(418)})
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("reconnected after a terminal state")
	}
}

func TestEndRoundClearsQR(t *testing.T) {
	w, r := newTestAdapter(t, false, func(context.Context) error { return nil })
	w.endRound()
	_, qrs := r.snapshot()
	if len(qrs) != 1 || qrs[0] != "" {
		t.Fatalf("qrs = %q, want one empty (clear) code", qrs)
	}
}

// connectRecorder is a connect that succeeds immediately (as ConnectContext does right
// after the handshake) and records when it was called.
type connectRecorder struct {
	mu    sync.Mutex
	calls []time.Time
}

func (c *connectRecorder) connect(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, time.Now())
	return nil
}
func (c *connectRecorder) count() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.calls) }
func (c *connectRecorder) last() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[len(c.calls)-1]
}

func waitCalls(t *testing.T, c *connectRecorder, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for c.count() < n {
		if time.Now().After(deadline) {
			t.Fatalf("connect calls = %d, want %d", c.count(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitIdle waits until no connect loop is running, so the next failure can start one.
func waitIdle(t *testing.T, w *Whatsmeow) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for w.connecting.Load() {
		if time.Now().After(deadline) {
			t.Fatal("connect loop still running")
		}
		time.Sleep(time.Millisecond)
	}
}

func cf() *events.ConnectFailure {
	return &events.ConnectFailure{Reason: events.ConnectFailureReason(418)}
}

func TestConnectFailureBacksOffAndGivesUp(t *testing.T) {
	c := &connectRecorder{}
	w, r := newTestAdapter(t, true, c.connect)
	w.failureBase = 40 * time.Millisecond

	start := time.Now()
	w.handle(cf()) // 1st: reconnect after 40ms
	waitCalls(t, c, 1)
	if d := c.last().Sub(start); d < 40*time.Millisecond {
		t.Fatalf("first reconnect after %s, want >= 40ms", d)
	}
	waitIdle(t, w)
	start = time.Now()
	w.handle(cf()) // 2nd: reconnect only after the doubled delay
	waitCalls(t, c, 2)
	if d := c.last().Sub(start); d < 80*time.Millisecond {
		t.Fatalf("second reconnect after %s, want >= 80ms", d)
	}
	waitIdle(t, w)
	for i := 3; i <= 4; i++ {
		w.handle(cf())
		waitCalls(t, c, i)
		waitIdle(t, w)
	}
	w.handle(cf()) // 5th consecutive: give up
	time.Sleep(400 * time.Millisecond)
	if n := c.count(); n != 4 {
		t.Fatalf("connect calls = %d after the 5th failure, want 4", n)
	}
	states, _ := r.snapshot()
	if last := states[len(states)-1]; last != (connEvt{"failed", "connect_failure_418"}) {
		t.Fatalf("last state = %v, want failed/connect_failure_418", last)
	}
	if !w.terminal.Load() {
		t.Fatal("giving up must be terminal")
	}
}

func TestConnectedResetsConnectFailureCount(t *testing.T) {
	c := &connectRecorder{}
	w, r := newTestAdapter(t, true, c.connect)
	for i := 1; i <= 4; i++ {
		w.handle(cf())
		waitCalls(t, c, i)
		waitIdle(t, w)
	}
	w.handle(&events.Connected{})
	start := time.Now()
	w.handle(cf()) // counts as the first failure again, with the base delay
	waitCalls(t, c, 5)
	if d := c.last().Sub(start); d > 150*time.Millisecond {
		t.Fatalf("delay not reset: reconnect after %s", d)
	}
	states, _ := r.snapshot()
	if last := states[len(states)-1]; last.state != "reconnecting" {
		t.Fatalf("last state = %v, want reconnecting", last)
	}
	if w.terminal.Load() {
		t.Fatal("a Connected in between must reset the failure count")
	}
}

func TestConnectFailureThroughProxyIsProxyUnreachable(t *testing.T) {
	var calls atomic.Int32
	w, r := newTestAdapter(t, true, func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		}
		return nil
	})
	w.proxied = true
	w.startConnectLoop(w.loopCtx, false, 0)
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	states, _ := r.snapshot()
	if len(states) == 0 || states[0] != (connEvt{"reconnecting", "proxy_unreachable"}) {
		t.Fatalf("states = %v", states)
	}
	// Without a proxy the same error is a plain connect failure.
	w.proxied = false
	if got := w.connectFailReason(&net.OpError{Op: "dial", Err: errors.New("x")}); got != "connect_failed" {
		t.Fatalf("reason = %q", got)
	}
}
