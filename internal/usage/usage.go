// Package usage counts what managed hosting bills and monitors (cloud spec E11, E3, E6):
// daily usage per instance persisted in SQLite, process counters for /metrics, and the
// instance.proxy_failed event.
package usage

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

const dayLayout = "2006-01-02"

// FlushInterval is how often counters reach the database (a crash loses at most this much).
const FlushInterval = 30 * time.Second

// Recorder is an events.Sink: it counts API sends that reached WhatsApp, received
// messages, attachment bytes and connected time per instance and UTC day, and flushes
// them to the store. Enqueue never blocks on I/O.
type Recorder struct {
	Store *store.Store
	Log   *slog.Logger
	// Now is the clock (default time.Now); tests replace it.
	Now func() time.Time

	mu        sync.Mutex
	pending   map[key]*store.Usage
	connected map[string]time.Time // instance -> start of the not yet accounted connected time

	sent, received, mediaBytes atomic.Int64
	transitions                sync.Map // state -> *atomic.Int64

	stop chan struct{}
	done chan struct{}
}

type key struct{ instance, day string }

var _ events.Sink = (*Recorder)(nil)

func (r *Recorder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Recorder) bucket(instanceID string, at time.Time) *store.Usage {
	if r.pending == nil {
		r.pending = map[key]*store.Usage{}
	}
	k := key{instanceID, at.UTC().Format(dayLayout)}
	u := r.pending[k]
	if u == nil {
		u = &store.Usage{InstanceID: k.instance, Day: k.day}
		r.pending[k] = u
	}
	return u
}

// eventData is the part of message.sent, message.received and instance.status we read.
type eventData struct {
	Source string `json:"source"`
	Media  *struct {
		Size int64 `json:"size"`
	} `json:"media"`
	State string `json:"state"`
}

// Enqueue implements events.Sink.
func (r *Recorder) Enqueue(e events.Envelope) {
	switch e.Type {
	case events.TypeMessageSent, events.TypeMessageReceived, events.TypeInstanceStatus:
	default:
		return
	}
	var env struct {
		Data eventData `json:"data"`
	}
	if err := json.Unmarshal(e.JSON, &env); err != nil {
		return
	}
	d := env.Data
	at := e.Timestamp
	if at.IsZero() {
		at = r.now()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch e.Type {
	case events.TypeMessageSent:
		if d.Source != "api" { // billing counts sends made through LiteZap only
			return
		}
		r.sent.Add(1)
		u := r.bucket(e.InstanceID, at)
		u.MessagesSent++
		if d.Media != nil && d.Media.Size > 0 {
			u.MediaBytes += d.Media.Size
			r.mediaBytes.Add(d.Media.Size)
		}
	case events.TypeMessageReceived:
		r.received.Add(1)
		u := r.bucket(e.InstanceID, at)
		u.MessagesReceived++
		if d.Media != nil && d.Media.Size > 0 {
			u.MediaBytes += d.Media.Size
			r.mediaBytes.Add(d.Media.Size)
		}
	case events.TypeInstanceStatus:
		r.countTransition(d.State)
		if r.connected == nil {
			r.connected = map[string]time.Time{}
		}
		since, was := r.connected[e.InstanceID]
		if was {
			r.addConnected(e.InstanceID, since, at)
			delete(r.connected, e.InstanceID)
		}
		if instance.State(d.State) == instance.Connected {
			r.connected[e.InstanceID] = at
		}
	}
}

func (r *Recorder) countTransition(state string) {
	if state == "" {
		return
	}
	c, _ := r.transitions.LoadOrStore(state, new(atomic.Int64))
	c.(*atomic.Int64).Add(1)
}

// addConnected books [from, to) as connected time, split at UTC midnights. r.mu is held.
func (r *Recorder) addConnected(instanceID string, from, to time.Time) {
	from, to = from.UTC(), to.UTC()
	for from.Before(to) {
		y, m, d := from.Date()
		midnight := time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
		end := to
		if midnight.Before(end) {
			end = midnight
		}
		r.bucket(instanceID, from).ConnectedSeconds += int64(end.Sub(from) / time.Second)
		from = end
	}
}

// Flush writes the pending counters (connected time up to now included). On failure the
// counters stay pending for the next flush.
func (r *Recorder) Flush(ctx context.Context) error {
	now := r.now()
	r.mu.Lock()
	for id, since := range r.connected {
		if s := now.Sub(since); s >= time.Second {
			whole := since.Add(s.Truncate(time.Second))
			r.addConnected(id, since, whole)
			r.connected[id] = whole
		}
	}
	batch := make([]store.Usage, 0, len(r.pending))
	for _, u := range r.pending {
		batch = append(batch, *u)
	}
	r.pending = nil
	r.mu.Unlock()
	err := r.Store.AddUsage(ctx, batch)
	if err != nil {
		r.mu.Lock()
		for _, u := range batch {
			p := r.bucket(u.InstanceID, mustDay(u.Day))
			p.MessagesSent += u.MessagesSent
			p.MessagesReceived += u.MessagesReceived
			p.MediaBytes += u.MediaBytes
			p.ConnectedSeconds += u.ConnectedSeconds
		}
		r.mu.Unlock()
		return err
	}
	// Deliveries are counted from the outbox (today and yesterday: late retries).
	return r.Store.SyncWebhookUsage(ctx, now.UTC().AddDate(0, 0, -1).Format(dayLayout))
}

func mustDay(d string) time.Time {
	t, _ := time.Parse(dayLayout, d)
	return t
}

// Usage flushes, then returns the stored rows (api.UsageReader).
func (r *Recorder) Usage(ctx context.Context, from, to, instanceID string) ([]store.Usage, error) {
	if err := r.Flush(ctx); err != nil {
		return nil, err
	}
	return r.Store.ListUsage(ctx, from, to, instanceID)
}

// Start flushes every FlushInterval until Close.
func (r *Recorder) Start() {
	r.stop, r.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(r.done)
		t := time.NewTicker(FlushInterval)
		defer t.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-t.C:
				if err := r.Flush(context.Background()); err != nil && r.Log != nil {
					r.Log.Error("usage: flush", "err", err)
				}
			}
		}
	}()
}

// Close stops the loop and writes what is pending (call it before the store closes).
func (r *Recorder) Close(ctx context.Context) error {
	if r.stop != nil {
		close(r.stop)
		<-r.done
		r.stop = nil
	}
	return r.Flush(ctx)
}

// Counters are process totals since start (Prometheus counters).
type Counters struct {
	MessagesSent, MessagesReceived, MediaBytes int64
	// StateChanges counts instance.status events by state.
	StateChanges map[string]int64
}

func (r *Recorder) Counters() Counters {
	c := Counters{MessagesSent: r.sent.Load(), MessagesReceived: r.received.Load(), MediaBytes: r.mediaBytes.Load(),
		StateChanges: map[string]int64{}}
	r.transitions.Range(func(k, v any) bool {
		c.StateChanges[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	return c
}
