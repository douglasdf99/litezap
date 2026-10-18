package queue

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/store"
)

// Canceled reports whether a failed item's last_error says it was canceled.
func Canceled(lastErr string) bool { return strings.HasPrefix(lastErr, "canceled: ") }

// Cancel removes a queued item without sending it: it ends as failed ("canceled: ..."),
// its staged media is deleted, waiters wake up and message.status failed is published.
// store.ErrNotQueued when it is in flight or final, store.ErrNotFound when unknown.
func (q *Queues) Cancel(ctx context.Context, instanceID, id string) (store.Outbound, error) {
	o, err := q.store.CancelOutbound(ctx, instanceID, id, "removed from the queue by the API")
	if err != nil {
		return o, err
	}
	q.canceled(instanceID, o)
	return o, nil
}

// Clear cancels every queued item of an instance (items being sent are left alone) and
// returns how many were canceled.
func (q *Queues) Clear(ctx context.Context, instanceID string) (int, error) {
	list, err := q.store.CancelQueued(ctx, instanceID, "queue cleared by the API")
	for _, o := range list {
		q.canceled(instanceID, o)
	}
	return len(list), err
}

func (q *Queues) canceled(instanceID string, o store.Outbound) {
	var p Payload
	if json.Unmarshal([]byte(o.Payload), &p) == nil && p.MediaFile != "" {
		_ = os.Remove(p.MediaFile)
	}
	if pub := q.opt.Events; pub != nil {
		pub.Publish(instanceID, events.TypeMessageStatus, events.MessageStatus{ID: o.ID, Chat: o.ChatJID,
			Status: store.OutFailed, Error: o.LastError})
	}
	q.notify(instanceID, o.ID)
}
