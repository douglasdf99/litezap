package queue

import (
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/settings"
)

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func TestPacerInterval(t *testing.T) {
	s := settings.Defaults() // 3s + up to 2s jitter
	p := &pacer{rand: func() float64 { return 0.5 }}
	if d := p.wait(t0, s, false); d != 0 {
		t.Fatalf("first send waits %v", d)
	}
	p.record(t0, s)
	if d := p.wait(t0.Add(time.Second), s, false); d != 3*time.Second { // 3s + 1s jitter - 1s elapsed
		t.Fatalf("wait = %v", d)
	}
	if d := p.wait(t0.Add(time.Second), s, true); d != 6*time.Second { // doubled interval
		t.Fatalf("double wait = %v", d)
	}
	if d := p.wait(t0.Add(time.Minute), s, false); d != 0 {
		t.Fatalf("late wait = %v", d)
	}
}

func TestPacerMaxPerMinute(t *testing.T) {
	s := settings.Defaults()
	s.MinIntervalMS, s.JitterMS, s.MaxPerMinute = 0, 0, 3
	p := &pacer{rand: func() float64 { return 0 }}
	for i := 0; i < 3; i++ {
		at := t0.Add(time.Duration(i) * time.Second)
		if d := p.wait(at, s, false); d != 0 {
			t.Fatalf("send %d waits %v", i, d)
		}
		p.record(at, s)
	}
	if d := p.wait(t0.Add(3*time.Second), s, false); d != 57*time.Second {
		t.Fatalf("4th send waits %v, want 57s", d)
	}
}

func TestDelays(t *testing.T) {
	if typingDelay("oi") != 100*time.Millisecond || typingDelay(string(make([]rune, 1000))) != 5*time.Second {
		t.Fatal("typing delay")
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second}
	for i, w := range want {
		if got := retryDelay(i + 1); got != w {
			t.Errorf("retryDelay(%d) = %v, want %v", i+1, got, w)
		}
	}
	if retryDelay(30) != 5*time.Minute {
		t.Fatal("retry delay cap")
	}
}
