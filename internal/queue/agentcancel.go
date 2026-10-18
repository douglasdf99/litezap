package queue

import (
	"context"

	"github.com/douglasdf99/litezap/internal/store"
)

// CancelWithReason is Cancel with a custom reason (last_error "canceled: <reason>"); the
// agent hook uses it to drop its queued replies when a human takes over a chat.
func (q *Queues) CancelWithReason(ctx context.Context, instanceID, id, reason string) (store.Outbound, error) {
	o, err := q.store.CancelOutbound(ctx, instanceID, id, reason)
	if err != nil {
		return o, err
	}
	q.canceled(instanceID, o)
	return o, nil
}
