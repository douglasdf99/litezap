package clock

import (
	"testing"
	"time"
)

func TestFakeTimers(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	f := NewFake(t0)
	a, b := f.NewTimer(time.Second), f.NewTimer(time.Minute)
	if f.Waiters() != 2 {
		t.Fatalf("waiters = %d", f.Waiters())
	}
	f.Advance(2 * time.Second)
	select {
	case got := <-a.C():
		if !got.Equal(t0.Add(2 * time.Second)) {
			t.Fatalf("fired at %v", got)
		}
	default:
		t.Fatal("1s timer did not fire")
	}
	select {
	case <-b.C():
		t.Fatal("1m timer fired early")
	default:
	}
	b.Stop()
	if f.Waiters() != 0 {
		t.Fatalf("waiters after stop = %d", f.Waiters())
	}
	if z := f.NewTimer(0); f.Waiters() != 0 || len(z.C()) != 1 {
		t.Fatal("a zero timer fires immediately")
	}
	r := Real{}.NewTimer(time.Millisecond)
	<-r.C()
	r.Stop()
}
