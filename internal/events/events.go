// Package events is the in-process event bus (spec section 5): producers publish
// versioned envelopes; webhooks (a fixed sink) and SSE streams (dynamic subscriptions)
// consume them. Publish never blocks, so it is safe to call with locks held.
package events

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/secure"
)

// Version is the current envelope version.
const Version = 1

// Event types of catalog v1 (spec 5.2). chat.presence and group.updated are not produced
// yet: the worker does not report them.
const (
	TypeInstanceStatus   = "instance.status"
	TypeInstanceQR       = "instance.qr"
	TypeInstancePairCode = "instance.pair_code"
	TypeMessageReceived  = "message.received"
	TypeMessageSent      = "message.sent"
	TypeMessageStatus    = "message.status"
	TypeMessageReaction  = "message.reaction"
	TypeMessageEdited    = "message.edited"
	TypeMessageRevoked   = "message.revoked"
	TypeMessagePollVote  = "message.poll_vote"
	TypeHistorySynced    = "history.synced"
)

// Types lists every type a filter may name.
var Types = []string{
	TypeInstanceStatus, TypeInstanceQR, TypeInstancePairCode, TypeMessageReceived,
	TypeMessageSent, TypeMessageStatus, TypeMessageReaction, TypeMessageEdited, TypeMessageRevoked,
	TypeMessagePollVote, TypeHistorySynced,
}

// Envelope is a published event. JSON is its encoding (spec 5.1), shared by every
// consumer: never modify it.
type Envelope struct {
	ID         string
	Type       string
	Version    int
	InstanceID string
	Timestamp  time.Time
	JSON       []byte
}

type wireEnvelope struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Version    int       `json:"version"`
	InstanceID string    `json:"instance_id"`
	Timestamp  time.Time `json:"timestamp"`
	Data       any       `json:"data"`
}

// Match reports whether an event type matches a filter pattern: "*" (everything),
// "prefix.*" (a family) or an exact type.
func Match(pattern, typ string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasSuffix(pattern, ".*"):
		return strings.HasPrefix(typ, pattern[:len(pattern)-1])
	default:
		return pattern != "" && pattern == typ
	}
}

// MatchAny reports whether typ matches one of the patterns.
func MatchAny(patterns []string, typ string) bool {
	for _, p := range patterns {
		if Match(p, typ) {
			return true
		}
	}
	return false
}

// ValidPattern reports whether p is "*", a known "family.*" or a known type.
func ValidPattern(p string) bool {
	if p == "*" {
		return true
	}
	for _, t := range Types {
		if p == t || (strings.HasSuffix(p, ".*") && strings.HasPrefix(t, p[:len(p)-1])) {
			return true
		}
	}
	return false
}

// Sink receives every event synchronously. Enqueue must not block nor call back into
// the publisher (it may run with a Manager lock held).
type Sink interface {
	Enqueue(Envelope)
}

// Filter selects the events of a subscription. Empty fields match everything.
type Filter struct {
	InstanceID string
	Types      []string
}

func (f Filter) match(e Envelope) bool {
	if f.InstanceID != "" && f.InstanceID != e.InstanceID {
		return false
	}
	return len(f.Types) == 0 || MatchAny(f.Types, e.Type)
}

// Bus fans events out to sinks and subscriptions.
type Bus struct {
	clock clock.Clock
	sinks []Sink
	log   *slog.Logger

	mu     sync.RWMutex
	subs   map[*Subscription]struct{}
	closed bool
}

// NewBus returns a bus; a nil clock means the wall clock.
func NewBus(c clock.Clock, sinks ...Sink) *Bus {
	if c == nil {
		c = clock.Real{}
	}
	return &Bus{clock: c, sinks: sinks, log: slog.New(slog.DiscardHandler), subs: map[*Subscription]struct{}{}}
}

// SetLogger sets the logger for encoding failures and dropped subscribers.
func (b *Bus) SetLogger(l *slog.Logger) { b.log = l }

// Publish builds the envelope and hands it to every sink and matching subscription.
// It never blocks: a subscription whose buffer is full is closed (Dropped reports it).
func (b *Bus) Publish(instanceID, typ string, data any) {
	e := Envelope{ID: secure.NewID("evt_"), Type: typ, Version: Version, InstanceID: instanceID,
		Timestamp: b.clock.Now().UTC()}
	js, err := json.Marshal(wireEnvelope{e.ID, e.Type, e.Version, e.InstanceID, e.Timestamp, data})
	if err != nil {
		b.log.Error("encode event", "type", typ, "instance_id", instanceID, "err", err)
		return
	}
	e.JSON = js
	for _, s := range b.sinks {
		s.Enqueue(e)
	}
	var slow []*Subscription
	b.mu.RLock()
	for s := range b.subs {
		if !s.filter.match(e) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			slow = append(slow, s)
		}
	}
	b.mu.RUnlock()
	for _, s := range slow {
		s.dropped.Store(true)
		b.remove(s)
		b.log.Warn("event subscriber too slow; disconnected", "instance_id", s.filter.InstanceID)
	}
}

// Subscribe opens a subscription buffering up to buf events. After CloseSubscribers the
// returned subscription is already closed.
func (b *Bus) Subscribe(f Filter, buf int) *Subscription {
	s := &Subscription{bus: b, filter: f, ch: make(chan Envelope, buf)}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(s.ch)
		s.gone = true
		return s
	}
	b.subs[s] = struct{}{}
	return s
}

func (b *Bus) remove(s *Subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s.gone {
		return
	}
	s.gone = true
	delete(b.subs, s)
	close(s.ch)
}

// CloseSubscribers ends every subscription (server shutdown); sinks keep receiving.
func (b *Bus) CloseSubscribers() {
	b.mu.Lock()
	b.closed = true
	subs := make([]*Subscription, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()
	for _, s := range subs {
		b.remove(s)
	}
}

// Subscribers is the number of open subscriptions.
func (b *Bus) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Subscription is a stream of events. Its channel closes when the subscription ends
// (Close, too slow, or CloseSubscribers); buffered events can still be read.
type Subscription struct {
	bus     *Bus
	filter  Filter
	ch      chan Envelope
	gone    bool // guarded by bus.mu
	dropped atomic.Bool
}

func (s *Subscription) C() <-chan Envelope { return s.ch }

// Dropped reports whether the subscription was closed for not keeping up.
func (s *Subscription) Dropped() bool { return s.dropped.Load() }

// Close ends the subscription; safe to call more than once.
func (s *Subscription) Close() { s.bus.remove(s) }
