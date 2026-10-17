package manager

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	base, max := time.Second, 5*time.Minute
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, w := range want {
		if got := Backoff(i, base, max); got != w {
			t.Errorf("attempt %d = %s, want %s", i, got, w)
		}
	}
	if got := Backoff(30, base, max); got != max {
		t.Errorf("capped = %s", got)
	}
}

func TestBreaker(t *testing.T) {
	b := Breaker{Max: 3, Window: time.Minute}
	t0 := time.Now()
	if b.Record(t0) || b.Record(t0.Add(10*time.Second)) {
		t.Fatal("opened too early")
	}
	if !b.Record(t0.Add(20 * time.Second)) {
		t.Fatal("should open at 3 crashes inside window")
	}
	b2 := Breaker{Max: 3, Window: time.Minute}
	b2.Record(t0)
	b2.Record(t0.Add(30 * time.Second))
	if b2.Record(t0.Add(2 * time.Minute)) {
		t.Fatal("old crashes must fall out of the window")
	}
}
