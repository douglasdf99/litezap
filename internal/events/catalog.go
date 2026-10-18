package events

import (
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/instance"
)

// Publisher is what producers need from the bus (nil-safe helpers below accept it).
type Publisher interface {
	Publish(instanceID, typ string, data any)
}

// InstanceStatus is the data of instance.status.
type InstanceStatus struct {
	State  instance.State `json:"state"`
	Reason string         `json:"reason,omitempty"`
}

// InstanceQR is the data of instance.qr.
type InstanceQR struct {
	Code      string `json:"code"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
}

// PairCode is the data of instance.pair_code.
type PairCode struct {
	Code string `json:"code"`
}

// Media describes an attachment (received media can be downloaded from URL).
type Media struct {
	ID       string `json:"id,omitempty"`
	Kind     string `json:"kind"`
	Mime     string `json:"mime,omitempty"`
	Size     int64  `json:"size,omitempty"`
	FileName string `json:"file_name,omitempty"`
	Seconds  int    `json:"seconds,omitempty"`
	PTT      bool   `json:"ptt,omitempty"`
	URL      string `json:"url,omitempty"`
}

// Message is the data of message.received, message.sent and message.reaction. Its
// fields follow the REST message view.
type Message struct {
	ID          string    `json:"id"` // LiteZap message id (for API sends, the queue item id)
	WAMessageID string    `json:"wa_message_id,omitempty"`
	Chat        string    `json:"chat"`
	Sender      string    `json:"sender,omitempty"`
	FromMe      bool      `json:"from_me"`
	IsGroup     bool      `json:"is_group,omitempty"`
	PushName    string    `json:"push_name,omitempty"`
	Source      string    `json:"source"` // contact | api | device
	Type        string    `json:"type"`
	Text        string    `json:"text,omitempty"` // body, caption or reaction emoji
	Media       *Media    `json:"media,omitempty"`
	ReactionTo  string    `json:"reaction_to,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
}

// MessageStatus is the data of message.status.
type MessageStatus struct {
	ID          string `json:"id"`
	WAMessageID string `json:"wa_message_id,omitempty"`
	Chat        string `json:"chat"`
	Status      string `json:"status"` // sent | delivered | read | failed
	Error       string `json:"error,omitempty"`
}

// HistorySynced is the data of history.synced.
type HistorySynced struct {
	Chats       int `json:"chats"`        // history blocks ingested (about one per chat)
	Messages    int `json:"messages"`     // messages in those blocks
	NewMessages int `json:"new_messages"` // messages not already in the log
}

// Observer is a manager.Observer publishing instance.status and instance.qr. It never
// blocks and never calls the Manager.
type Observer struct{ Bus Publisher }

func (o Observer) StateChanged(id string, st instance.State, reason string) {
	o.Bus.Publish(id, TypeInstanceStatus, InstanceStatus{State: st, Reason: reason})
}

func (o Observer) WorkerEvent(id string, ev *workerv1.Event) {
	if qr := ev.GetQr(); qr != nil && qr.Code != "" { // an empty code only ends a pairing round
		o.Bus.Publish(id, TypeInstanceQR, InstanceQR{Code: qr.Code, TimeoutMS: qr.TimeoutMs})
	}
}

// HistoryTracker turns the many history chunks of a sync into one history.synced per
// instance, published once no chunk arrived for the quiet period (the worker does not
// mark the end of a sync).
type HistoryTracker struct {
	pub   Publisher
	clock clock.Clock
	quiet time.Duration

	mu      sync.Mutex
	pending map[string]*histPending
	closed  bool
	wake    chan struct{}
	done    chan struct{}
	exited  chan struct{}
}

type histPending struct {
	counts HistorySynced
	due    time.Time
}

// NewHistoryTracker starts a tracker; call Close to stop it (pending counts are published).
func NewHistoryTracker(pub Publisher, c clock.Clock, quiet time.Duration) *HistoryTracker {
	if c == nil {
		c = clock.Real{}
	}
	h := &HistoryTracker{pub: pub, clock: c, quiet: quiet, pending: map[string]*histPending{},
		wake: make(chan struct{}, 1), done: make(chan struct{}), exited: make(chan struct{})}
	go h.run()
	return h
}

// Add records one ingested history chunk.
func (h *HistoryTracker) Add(instanceID string, messages, newMessages int) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	p := h.pending[instanceID]
	if p == nil {
		p = &histPending{}
		h.pending[instanceID] = p
	}
	p.counts.Chats++
	p.counts.Messages += messages
	p.counts.NewMessages += newMessages
	p.due = h.clock.Now().Add(h.quiet)
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// Close stops the tracker and publishes whatever is pending.
func (h *HistoryTracker) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.mu.Unlock()
	close(h.done)
	<-h.exited // a flush in progress finishes first: nothing is published after Close
	h.flush(true)
}

func (h *HistoryTracker) run() {
	defer close(h.exited)
	for {
		next := h.flush(false)
		var timer clock.Timer
		var tc <-chan time.Time
		if !next.IsZero() {
			timer = h.clock.NewTimer(next.Sub(h.clock.Now()))
			tc = timer.C()
		}
		select {
		case <-h.done:
		case <-h.wake:
		case <-tc:
		}
		if timer != nil {
			timer.Stop()
		}
		select {
		case <-h.done:
			return
		default:
		}
	}
}

// flush publishes the due entries (all with force) and returns the next due time.
func (h *HistoryTracker) flush(force bool) time.Time {
	now := h.clock.Now()
	var out []struct {
		id string
		c  HistorySynced
	}
	var next time.Time
	h.mu.Lock()
	for id, p := range h.pending {
		if force || !p.due.After(now) {
			out = append(out, struct {
				id string
				c  HistorySynced
			}{id, p.counts})
			delete(h.pending, id)
		} else if next.IsZero() || p.due.Before(next) {
			next = p.due
		}
	}
	h.mu.Unlock()
	for _, o := range out {
		h.pub.Publish(o.id, TypeHistorySynced, o.c)
	}
	return next
}
