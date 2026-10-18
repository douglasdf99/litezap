package service

import (
	"context"
	"errors"

	"github.com/douglasdf99/litezap/internal/store"
)

// CancelAgentReplies cancels the agent hook's replies and reactions still queued for a
// chat (a human took over, the loop guard tripped, or the agent handed the chat off), so
// a paced reply never lands after that. Items already being sent are left alone.
func (s *Messaging) CancelAgentReplies(ctx context.Context, instanceID, chat, reason string) (int, error) {
	ids, err := s.Store.QueuedAgentItems(ctx, instanceID, chat)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		_, err := s.Queues.CancelWithReason(ctx, instanceID, id, reason)
		switch {
		case err == nil:
			n++
		case errors.Is(err, store.ErrNotQueued), errors.Is(err, store.ErrNotFound): // sent meanwhile
		default:
			return n, err
		}
	}
	return n, nil
}
