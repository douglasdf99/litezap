// Package autoread marks received messages as read automatically when an instance has
// auto_read enabled (marco 9). It is a manager.Observer: each received message joins a
// batch per chat (and sender, in groups) that is read after a short human-like delay,
// with at most one read receipt per instance per Gap.
package autoread

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/settings"
)

// Workers gives the RPC client of a running instance (the manager).
type Workers interface {
	Client(id string) (workerv1.WorkerClient, error)
}

// Unread clears a chat's unread count once it was read (the store).
type Unread interface {
	ResetUnread(ctx context.Context, instanceID, chatJID string) error
}

// Reader is the auto-read observer. Set the fields before use; call Close on shutdown.
type Reader struct {
	Workers  Workers
	Unread   Unread
	Settings func(ctx context.Context, instanceID string) (settings.Settings, error)
	Log      *slog.Logger
	// MinDelay/MaxDelay bound the delay before a batch is read (default 2s-5s); Gap is the
	// minimum interval between receipts of one instance (default 1s).
	MinDelay, MaxDelay, Gap time.Duration

	mu      sync.Mutex
	closed  bool
	pending map[key]*batch
	next    map[string]time.Time // instance -> earliest next receipt
	done    chan struct{}        // closed by Close
	wg      sync.WaitGroup
}

// doneCh must be called with r.mu held.
func (r *Reader) doneCh() chan struct{} {
	if r.done == nil {
		r.done = make(chan struct{})
	}
	return r.done
}

type key struct{ instance, chat, sender string }

type batch struct {
	ids   []string
	timer *time.Timer
}

// maxBatch bounds the ids of one receipt (later ones start a new batch).
const maxBatch = 100

func (r *Reader) StateChanged(string, instance.State, string) {}

// skipped are message types that get no read receipt (updates of other messages).
func skipped(m *workerv1.Message) bool {
	switch m.Type {
	case "reaction", "edit", "revoke", "poll_vote", "event_response", "unknown":
		return true
	}
	return m.FromMe || m.Id == "" || m.Chat == "" || strings.HasSuffix(m.Chat, "@broadcast") ||
		strings.HasSuffix(m.Chat, "@newsletter")
}

func (r *Reader) delay() time.Duration {
	lo, hi := r.MinDelay, r.MaxDelay
	if lo <= 0 {
		lo = 2 * time.Second
	}
	if hi < lo {
		hi = lo + 3*time.Second
	}
	return lo + time.Duration(rand.Int64N(int64(hi-lo)+1))
}

func (r *Reader) WorkerEvent(id string, ev *workerv1.Event) {
	m := ev.GetMessage()
	if m == nil || skipped(m) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	set, err := r.Settings(ctx, id)
	cancel()
	if err != nil || !set.AutoRead.Enabled || (m.IsGroup && !set.AutoRead.Groups) {
		return
	}
	k := key{instance: id, chat: m.Chat}
	if m.IsGroup {
		k.sender = m.Sender
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if r.pending == nil {
		r.pending = map[key]*batch{}
	}
	b := r.pending[k]
	if b == nil || len(b.ids) >= maxBatch {
		if b != nil { // full: read it now, start another
			b.timer.Stop()
			r.startFlush(k, b)
		}
		b = &batch{}
		r.pending[k] = b
		b.timer = time.AfterFunc(r.delay(), func() {
			r.mu.Lock()
			cur := r.pending[k]
			if cur != b || r.closed {
				r.mu.Unlock()
				return
			}
			delete(r.pending, k)
			r.startFlush(k, b)
			r.mu.Unlock()
		})
	}
	b.ids = append(b.ids, m.Id)
}

// startFlush must be called with r.mu held.
func (r *Reader) startFlush(k key, b *batch) {
	gap := r.Gap
	if gap <= 0 {
		gap = time.Second
	}
	if r.next == nil {
		r.next = map[string]time.Time{}
	}
	now := time.Now()
	at := r.next[k.instance]
	if at.Before(now) {
		at = now
	}
	r.next[k.instance] = at.Add(gap)
	ids := append([]string(nil), b.ids...)
	done := r.doneCh()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTimer(time.Until(at))
		defer t.Stop()
		select {
		case <-t.C:
			r.send(k, ids)
		case <-done:
		}
	}()
}

func (r *Reader) send(k key, ids []string) {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return
	}
	cli, err := r.Workers.Client(k.instance)
	if err != nil {
		return // not connected: the messages stay unread
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := cli.MarkRead(ctx, &workerv1.MarkReadRequest{Chat: k.chat, Sender: k.sender, MessageIds: ids}); err != nil {
		if r.Log != nil {
			r.Log.Warn("auto read failed", "instance", k.instance, "err", err)
		}
		return
	}
	if r.Unread != nil {
		_ = r.Unread.ResetUnread(ctx, k.instance, k.chat)
	}
}

// Close drops pending batches and waits for receipts being sent.
func (r *Reader) Close() {
	r.mu.Lock()
	if !r.closed {
		close(r.doneCh())
	}
	r.closed = true
	for k, b := range r.pending {
		b.timer.Stop()
		delete(r.pending, k)
	}
	r.mu.Unlock()
	r.wg.Wait()
}
