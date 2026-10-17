package worker

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
