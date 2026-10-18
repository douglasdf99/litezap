package usage

import (
	"sync"
	"sync/atomic"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
)

// ProxyUnreachable is the state_reason the worker reports when its proxy fails.
const ProxyUnreachable = "proxy_unreachable"

// DefaultProxyRepeat is how often instance.proxy_failed is repeated during an outage.
const DefaultProxyRepeat = 5 * time.Minute

// ProxyWatch is a manager.Observer that publishes instance.proxy_failed (cloud E6) when
// an instance starts failing to reach WhatsApp through its proxy, and repeats it while
// the outage lasts (Tick). It never blocks and never calls the Manager.
type ProxyWatch struct {
	Bus    events.Publisher
	Repeat time.Duration // default DefaultProxyRepeat
	Now    func() time.Time

	mu      sync.Mutex
	outages map[string]*outage
	details map[string]string      // last connect error reported with proxy_unreachable
	starts  map[string][]time.Time // outage starts in the last hour, per instance

	failures atomic.Int64

	stop chan struct{}
	done chan struct{}
}

type outage struct {
	since, last time.Time
	repeat      int
}

func (p *ProxyWatch) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// WorkerEvent keeps the error text the worker sends with a proxy_unreachable state (the
// manager forwards connection states that carry a detail; StateChanged follows).
func (p *ProxyWatch) WorkerEvent(id string, ev *workerv1.Event) {
	c := ev.GetConn()
	if c == nil || c.Reason != ProxyUnreachable || c.Detail == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.details == nil {
		p.details = map[string]string{}
	}
	p.details[id] = c.Detail
}

func (p *ProxyWatch) StateChanged(id string, st instance.State, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.outages == nil {
		p.outages, p.starts = map[string]*outage{}, map[string][]time.Time{}
	}
	if reason != ProxyUnreachable {
		delete(p.outages, id)
		delete(p.details, id)
		return
	}
	if _, ok := p.outages[id]; ok {
		return
	}
	now := p.now()
	o := &outage{since: now, last: now}
	p.outages[id] = o
	recent := p.starts[id][:0]
	for _, t := range p.starts[id] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	p.starts[id] = append(recent, now)
	p.failures.Add(1)
	p.publish(id, o, now)
}

// publish sends the event for o; p.mu is held.
func (p *ProxyWatch) publish(id string, o *outage, now time.Time) {
	if p.Bus == nil {
		return
	}
	p.Bus.Publish(id, events.TypeInstanceProxyFailed, events.InstanceProxyFailed{
		Reason: ProxyUnreachable, Since: o.since.UTC(), DurationSeconds: int64(now.Sub(o.since) / time.Second),
		OutagesLastHour: len(p.starts[id]), Repeat: o.repeat, Error: p.details[id],
	})
}

// Tick repeats the event for outages last notified at least Repeat ago.
func (p *ProxyWatch) Tick() {
	every := p.Repeat
	if every <= 0 {
		every = DefaultProxyRepeat
	}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, o := range p.outages {
		if now.Sub(o.last) >= every {
			o.repeat++
			o.last = now
			p.publish(id, o, now)
		}
	}
}

// Failures counts proxy outages since start (for /metrics).
func (p *ProxyWatch) Failures() int64 { return p.failures.Load() }

// Unreachable is the number of instances currently in a proxy outage.
func (p *ProxyWatch) Unreachable() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.outages)
}

// Start ticks every 30s until Close.
func (p *ProxyWatch) Start() {
	p.stop, p.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(p.done)
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-t.C:
				p.Tick()
			}
		}
	}()
}

func (p *ProxyWatch) Close() {
	if p.stop != nil {
		close(p.stop)
		<-p.done
		p.stop = nil
	}
}
