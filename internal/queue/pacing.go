package queue

import (
	"time"

	"github.com/douglasdf99/litezap/internal/settings"
)

// pacer spaces sends of one instance (spec 4.1): min_interval + random jitter between
// sends (twice the interval for an identical text to another new recipient) and at most
// max_per_minute sends in any minute.
type pacer struct {
	rand   func() float64
	last   time.Time
	jitter time.Duration // drawn when last was recorded
	recent []time.Time   // sends of the last minute
}

// record notes a send attempt at t.
func (p *pacer) record(t time.Time, s settings.Settings) {
	p.last = t
	p.jitter = time.Duration(p.rand() * float64(s.Jitter()))
	p.recent = append(p.recent, t)
}

// wait is how long to wait at now before the next send.
func (p *pacer) wait(now time.Time, s settings.Settings, double bool) time.Duration {
	var d time.Duration
	if !p.last.IsZero() {
		gap := s.MinInterval()
		if double {
			gap *= 2
		}
		d = p.last.Add(gap + p.jitter).Sub(now)
	}
	cut := now.Add(-time.Minute)
	keep := p.recent[:0]
	for _, t := range p.recent {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	p.recent = keep
	if s.MaxPerMinute > 0 && len(p.recent) >= s.MaxPerMinute {
		if w := p.recent[len(p.recent)-s.MaxPerMinute].Add(time.Minute).Sub(now); w > d {
			d = w
		}
	}
	return max(d, 0)
}

// typingDelay simulates typing: ~50ms per character, at most 5s.
func typingDelay(text string) time.Duration {
	return min(time.Duration(len([]rune(text)))*50*time.Millisecond, 5*time.Second)
}

// retryDelay is the backoff before attempt n+1 after n failed attempts: 5s, 10s, 20s, 40s...
func retryDelay(attempts int) time.Duration {
	d := 5 * time.Second
	for i := 1; i < attempts && d < 5*time.Minute; i++ {
		d *= 2
	}
	return min(d, 5*time.Minute)
}
