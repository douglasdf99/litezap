package worker

import (
	"context"
	"sync"
	"time"
)

// ZombieReason is the state_reason reported when the watchdog forces a reconnect.
const ZombieReason = "zombie_watchdog"

// watchdog is the connection watchdog (engine spec 9.2, cloud E13): a logged-in worker
// whose reported state disagrees with its socket for longer than timeout — "connected"
// without a socket, or stuck in keepalive_timeout while the socket still looks open — is
// forced to reconnect. whatsmeow handles the usual cases itself; this is the backstop
// for a missed event or a wedged socket, which would otherwise look connected forever.
type watchdog struct {
	w           *Whatsmeow
	timeout     time.Duration
	isConnected func() bool
	now         func() time.Time

	mu            sync.Mutex
	state, reason string
	badSince      time.Time
}

// watchedSink records the last reported state for the watchdog.
type watchedSink struct {
	Sink
	d *watchdog
}

func (s watchedSink) ConnState(state, reason, jid string) {
	s.d.observe(state, reason)
	s.Sink.ConnState(state, reason, jid)
}

// withWatchdog wraps sink and starts the watchdog when zombieTimeout is set (ctx ends it).
func (w *Whatsmeow) withWatchdog(ctx context.Context, sink Sink) Sink {
	if w.zombieTimeout <= 0 {
		return sink
	}
	d := &watchdog{w: w, timeout: w.zombieTimeout, isConnected: w.cli.IsConnected, now: time.Now}
	go d.run(ctx)
	return watchedSink{Sink: sink, d: d}
}

func (d *watchdog) observe(state, reason string) {
	d.mu.Lock()
	d.state, d.reason = state, reason
	d.badSince = time.Time{}
	d.mu.Unlock()
}

func (d *watchdog) run(ctx context.Context) {
	every := min(d.timeout/4, 30*time.Second)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.check()
		}
	}
}

// check forces a reconnect when the mismatch has lasted timeout; it reports whether it did.
func (d *watchdog) check() bool {
	w := d.w
	if w.terminal.Load() || w.cli.Store.ID == nil {
		return false
	}
	connected := d.isConnected()
	now := d.now()
	d.mu.Lock()
	bad := (d.state == "connected" && !connected) ||
		(d.state == "reconnecting" && d.reason == "keepalive_timeout" && connected)
	if !bad {
		d.badSince = time.Time{}
		d.mu.Unlock()
		return false
	}
	if d.badSince.IsZero() {
		d.badSince = now
		d.mu.Unlock()
		return false
	}
	if now.Sub(d.badSince) < d.timeout {
		d.mu.Unlock()
		return false
	}
	d.badSince = time.Time{}
	d.mu.Unlock()

	w.log.Warn("connection watchdog: forcing a reconnect", "state", d.state, "socket_connected", connected, "after", d.timeout)
	w.sink.ConnState("reconnecting", ZombieReason, w.jid())
	w.mu.Lock()
	ctx := w.loopCtx
	w.mu.Unlock()
	if ctx != nil && ctx.Err() == nil {
		w.startConnectLoop(ctx, true, 0)
	}
	return true
}
