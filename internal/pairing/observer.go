package pairing

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

// Observer is a manager.Observer that detects completed pairings (qr_pending, then
// connected) and publishes instance.paired, spending the instance's live pairing links.
// It never blocks the manager: the store work runs in a goroutine.
type Observer struct {
	Store  *store.Store
	Events events.Publisher
	Log    *slog.Logger
	Now    func() time.Time

	mu      sync.Mutex
	pairing map[string]bool // instances seen in qr_pending since their last connection
	wg      sync.WaitGroup
}

func (o *Observer) WorkerEvent(string, *workerv1.Event) {}

func (o *Observer) StateChanged(id string, st instance.State, _ string) {
	o.mu.Lock()
	if o.pairing == nil {
		o.pairing = map[string]bool{}
	}
	switch st {
	case instance.QRPending:
		o.pairing[id] = true
		o.mu.Unlock()
		return
	case instance.Connected:
		was := o.pairing[id]
		delete(o.pairing, id)
		o.mu.Unlock()
		if was {
			o.wg.Add(1)
			go o.paired(id)
		}
		return
	case instance.Starting, instance.Connecting, instance.Reconnecting:
		// part of a pairing in progress (or unrelated): keep what we know
	default: // stopped, logged out, failed, banned: the pairing did not complete
		delete(o.pairing, id)
	}
	o.mu.Unlock()
}

func (o *Observer) paired(id string) {
	defer o.wg.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Now()
	if o.Now != nil {
		now = o.Now()
	}
	n, err := o.Store.MarkPairingLinksUsed(ctx, id, now)
	if err != nil && o.Log != nil {
		o.Log.Error("pairing: spend links", "instance_id", id, "err", err)
	}
	data := events.InstancePaired{ViaLink: n > 0}
	if in, err := o.Store.GetInstance(ctx, id); err == nil {
		user, _, _ := strings.Cut(in.JID, "@")
		data.Phone, _, _ = strings.Cut(user, ":")
	}
	if o.Events != nil {
		o.Events.Publish(id, events.TypeInstancePaired, data)
	}
}

// Close waits for pending notifications (call before the store closes).
func (o *Observer) Close() { o.wg.Wait() }
