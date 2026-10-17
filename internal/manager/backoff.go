package manager

import "time"

// Backoff returns base * 2^attempt, capped at max.
func Backoff(attempt int, base, max time.Duration) time.Duration {
	d := base
	for i := 0; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

// Breaker opens after Max crashes inside Window.
type Breaker struct {
	Max    int
	Window time.Duration
	times  []time.Time
}

// Record registers a crash at now and reports whether the breaker is open.
func (b *Breaker) Record(now time.Time) bool {
	cut := now.Add(-b.Window)
	kept := b.times[:0]
	for _, t := range b.times {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	b.times = append(kept, now)
	return len(b.times) >= b.Max
}
