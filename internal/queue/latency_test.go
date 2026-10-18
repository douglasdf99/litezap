package queue

import (
	"sync"
	"testing"

	"github.com/douglasdf99/litezap/internal/instance"
)

type obsRec struct {
	mu sync.Mutex
	vs []float64
}

func (o *obsRec) Observe(v float64) { o.mu.Lock(); o.vs = append(o.vs, v); o.mu.Unlock() }
func (o *obsRec) n() int            { o.mu.Lock(); defer o.mu.Unlock(); return len(o.vs) }

func TestSendLatencyObserved(t *testing.T) {
	rec := &obsRec{}
	e := newEnvAt(t, t0, fastSettings(), instance.Connected, func(o *Options) { o.SendLatency = rec })
	e.enqueue(chatA, Payload{Type: "text", Text: "um"})
	e.enqueue(chatB, Payload{Type: "text", Text: "dois"})
	e.eventually("2 observations", func() bool { return rec.n() == 2 })
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, v := range rec.vs {
		if v < 0 {
			t.Fatalf("negative latency %v", v)
		}
	}
}
