// Package clock abstracts time so the send queue's pacing can be tested without sleeping.
package clock

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

type Timer interface {
	C() <-chan time.Time
	// Stop releases the timer; always call it (a Fake counts unstopped timers as waiters).
	Stop()
}

// Real is the wall clock.
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

func (Real) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop()               { r.t.Stop() }

// Fake is a manual clock: time only moves with Advance.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*fakeTimer]struct{}
}

type fakeTimer struct {
	f  *Fake
	at time.Time
	ch chan time.Time
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	delete(t.f.timers, t)
}

func NewFake(t time.Time) *Fake { return &Fake{now: t, timers: map[*fakeTimer]struct{}{}} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{f: f, at: f.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.ch <- f.now
		return t
	}
	f.timers[t] = struct{}{}
	return t
}

// Advance moves time forward and fires every timer that became due.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	for t := range f.timers {
		if !t.at.After(f.now) {
			t.ch <- f.now
			delete(f.timers, t)
		}
	}
}

// Waiters is the number of pending, unstopped timers: tests use it to know that a
// goroutine is blocked waiting for time to pass.
func (f *Fake) Waiters() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}
