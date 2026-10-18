package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/store"
)

// QueuePage is a page of GET /instances/{id}/queue.
type QueuePage struct {
	Items      []MessageView `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type queueCursor struct {
	Seq int64 `json:"s"`
}

// queueStatuses maps the status filter of the queue listing.
func queueStatuses(filter string) ([]string, error) {
	switch filter {
	case "":
		return []string{store.OutQueued, store.OutInFlight}, nil
	case store.OutQueued, store.OutInFlight, store.OutSent, store.OutFailed:
		return []string{filter}, nil
	case "all":
		return nil, nil
	}
	return nil, apierr.Validation("status must be queued, in_flight, sent, failed or all")
}

// Queue lists the instance's send queue, oldest first (default: items not sent yet).
func (s *Messaging) Queue(ctx context.Context, instanceID, status string, limit int, cursor string) (QueuePage, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return QueuePage{}, mapErr(err)
	}
	statuses, err := queueStatuses(status)
	if err != nil {
		return QueuePage{}, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var after queueCursor
	if cursor != "" {
		if err := decodeCursor(cursor, &after); err != nil {
			return QueuePage{}, err
		}
	}
	items, last, err := s.Store.ListOutbound(ctx, instanceID, statuses, limit, after.Seq)
	if err != nil {
		return QueuePage{}, err
	}
	page := QueuePage{Items: make([]MessageView, 0, len(items))}
	for _, o := range items {
		page.Items = append(page.Items, outboundView(o, nil))
	}
	if len(items) == limit {
		page.NextCursor = encodeCursor(queueCursor{Seq: last})
	}
	return page, nil
}

// CancelQueued removes a queued message before it is sent. A message already being sent
// (in_flight) or finished cannot be canceled (conflict).
func (s *Messaging) CancelQueued(ctx context.Context, instanceID, id string) (MessageView, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return MessageView{}, mapErr(err)
	}
	o, err := s.Queues.Cancel(ctx, instanceID, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return MessageView{}, notFound("queued message")
	case errors.Is(err, store.ErrNotQueued):
		cur, _ := s.Store.GetOutbound(ctx, instanceID, id)
		return MessageView{}, apierr.New(http.StatusConflict, "conflict",
			"the message is "+cur.Status+" and can no longer be removed from the queue", false)
	case err != nil:
		return MessageView{}, err
	}
	return outboundView(o, nil), nil
}

// ClearQueue cancels every queued message of the instance (messages being sent are left
// alone) and returns how many were removed.
func (s *Messaging) ClearQueue(ctx context.Context, instanceID string) (int, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return 0, mapErr(err)
	}
	return s.Queues.Clear(ctx, instanceID)
}
