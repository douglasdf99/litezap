package queue

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/instance"
)

// The operator's send suspension holds queued items until it is released and Wake runs.
func TestHoldPausesTheQueueUntilWoken(t *testing.T) {
	var held atomic.Bool
	held.Store(true)
	e := newEnvAt(t, t0, fastSettings(), instance.Connected, func(o *Options) {
		o.Hold = func(id string) bool { return id == "i1" && held.Load() }
	})
	e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	time.Sleep(50 * time.Millisecond)
	if n := len(e.fake.WA.Sent()); n != 0 {
		t.Fatalf("sent %d while held", n)
	}
	held.Store(false)
	e.q.Wake("i1")
	e.eventually("send after release", func() bool { return len(e.fake.WA.Sent()) == 1 })
}
