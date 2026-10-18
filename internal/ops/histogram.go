package ops

import (
	"fmt"
	"strconv"
	"sync"
)

// Histogram is a Prometheus histogram with fixed buckets (upper bounds, ascending).
type Histogram struct {
	bounds []float64

	mu     sync.Mutex
	counts []uint64 // per bucket (not cumulative)
	sum    float64
	total  uint64
}

// QueueLatencyBuckets fit the send queue: pacing keeps most sends within seconds to
// minutes; scheduled sends and send windows push some to hours.
var QueueLatencyBuckets = []float64{0.5, 1, 2.5, 5, 10, 30, 60, 300, 900, 3600, 21600}

func NewHistogram(bounds []float64) *Histogram {
	return &Histogram{bounds: bounds, counts: make([]uint64, len(bounds))}
}

// Observe records one value.
func (h *Histogram) Observe(v float64) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sum += v
	h.total++
	for i, b := range h.bounds {
		if v <= b {
			h.counts[i]++
			return
		}
	}
}

func (h *Histogram) write(m *writer, name, help string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m.help(name, "histogram", help)
	var cum uint64
	for i, b := range h.bounds {
		cum += h.counts[i]
		fmt.Fprintf(m.w, "%s_bucket{le=%q} %d\n", name, strconv.FormatFloat(b, 'g', -1, 64), cum)
	}
	fmt.Fprintf(m.w, "%s_bucket{le=\"+Inf\"} %d\n", name, h.total)
	fmt.Fprintf(m.w, "%s_sum %g\n%s_count %d\n", name, h.sum, name, h.total)
}
