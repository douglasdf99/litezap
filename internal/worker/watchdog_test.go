package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchdogForcesReconnectAfterTimeout(t *testing.T) {
	var connects atomic.Int32
	w, r := newTestAdapter(t, true, func(context.Context) error { connects.Add(1); return nil })
	var socket atomic.Bool
	now := time.Unix(1_000_000, 0)
	d := &watchdog{w: w, timeout: time.Minute, isConnected: socket.Load, now: func() time.Time { return now }}
	w.sink = watchedSink{Sink: r, d: d}
	d.sink = w.sink

	w.sink.ConnState("connected", "", "")
	socket.Store(true)
	if d.check() {
		t.Fatal("healthy connection flagged")
	}
	socket.Store(false) // socket gone, no Disconnected event
	if d.check() {
		t.Fatal("forced on first sight")
	}
	now = now.Add(30 * time.Second)
	if d.check() {
		t.Fatal("forced before the timeout")
	}
	now = now.Add(31 * time.Second)
	if !d.check() {
		t.Fatal("not forced after the timeout")
	}
	deadline := time.Now().Add(3 * time.Second)
	for connects.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if connects.Load() != 1 {
		t.Fatalf("connects = %d", connects.Load())
	}
	for w.connecting.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	states, _ := r.snapshot()
	if last := states[len(states)-1]; last != (connEvt{"reconnecting", ZombieReason}) {
		t.Fatalf("states = %v", states)
	}

	// Stuck in keepalive_timeout with the socket still "open".
	w.sink.ConnState("reconnecting", "keepalive_timeout", "")
	socket.Store(true)
	d.check()
	now = now.Add(2 * time.Minute)
	if !d.check() {
		t.Fatal("wedged keepalive not forced")
	}
	// A new state report resets the clock.
	w.sink.ConnState("connected", "", "")
	socket.Store(false)
	d.check()
	w.sink.ConnState("connected", "", "")
	now = now.Add(2 * time.Minute)
	if d.check() {
		t.Fatal("a fresh report must restart the timeout")
	}
}

func TestWatchdogIgnoresUnpairedAndTerminal(t *testing.T) {
	w, r := newTestAdapter(t, false, func(context.Context) error { return nil })
	now := time.Unix(1_000_000, 0)
	d := &watchdog{w: w, timeout: time.Minute, isConnected: func() bool { return false }, now: func() time.Time { return now }}
	w.sink = watchedSink{Sink: r, d: d}
	d.sink = w.sink
	w.sink.ConnState("connected", "", "")
	d.check()
	now = now.Add(time.Hour)
	if d.check() {
		t.Fatal("unpaired worker forced")
	}
	w2, r2 := newTestAdapter(t, true, func(context.Context) error { return nil })
	d2 := &watchdog{w: w2, timeout: time.Minute, isConnected: func() bool { return false }, now: func() time.Time { return now }}
	w2.sink = watchedSink{Sink: r2, d: d2}
	d2.sink = w2.sink
	w2.sink.ConnState("connected", "", "")
	w2.terminal.Store(true)
	d2.check()
	now = now.Add(time.Hour)
	if d2.check() {
		t.Fatal("terminal worker forced")
	}
}

func TestWithWatchdogOffByDefault(t *testing.T) {
	w, r := newTestAdapter(t, true, func(context.Context) error { return nil })
	if s := w.withWatchdog(context.Background(), r); s != Sink(r) {
		t.Fatal("watchdog enabled without zombie_timeout")
	}
	w.zombieTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, ok := w.withWatchdog(ctx, r).(watchedSink); !ok {
		t.Fatal("watchdog not enabled")
	}
}
