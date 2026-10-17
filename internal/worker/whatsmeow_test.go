package worker

import (
	"context"
	"sync"
	"testing"
	"time"

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
