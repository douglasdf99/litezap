package service

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
)

// Presence subscriptions per instance: each one asks WhatsApp for a contact's online
// status, and many of them look like scraping.
const (
	presenceSubsPerMinute = 20
	presenceSubsPerDay    = 200
)

// presenceQuota limits subscriptions per instance over a sliding minute and a rolling day.
type presenceQuota struct {
	mu   sync.Mutex
	hits map[string][]time.Time // instance -> subscriptions of the last 24h
}

// take reports whether one more subscription fits the quota (and counts it).
func (q *presenceQuota) take(instanceID string, now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.hits == nil {
		q.hits = map[string][]time.Time{}
	}
	var keep []time.Time
	minute := 0
	for _, t := range q.hits[instanceID] {
		if now.Sub(t) < 24*time.Hour {
			keep = append(keep, t)
			if now.Sub(t) < time.Minute {
				minute++
			}
		}
	}
	q.hits[instanceID] = keep
	if minute >= presenceSubsPerMinute || len(keep) >= presenceSubsPerDay {
		return false
	}
	q.hits[instanceID] = append(keep, now)
	return true
}

// SubscribePresence is refused while the operator suspends sending, like check-numbers:
// it reaches WhatsApp for a third party's data.
func (g GuardedMessaging) SubscribePresence(ctx context.Context, instanceID, to string) error {
	if err := g.Sending.Check(instanceID); err != nil {
		return err
	}
	return g.Messaging.SubscribePresence(ctx, instanceID, to)
}

// SubscribePresence asks WhatsApp for a contact's online/offline updates (chat.presence
// with state online/offline). It needs presence.online: WhatsApp only sends presence to
// accounts that are online. Subscriptions last until the connection drops.
func (s *Messaging) SubscribePresence(ctx context.Context, instanceID, to string) error {
	in, err := s.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return mapErr(err)
	}
	set, err := s.effective(in)
	if err != nil {
		return err
	}
	if !set.Presence.Online {
		return apierr.New(http.StatusConflict, "presence_offline",
			"presence updates need presence.online in the instance settings (the phone stops showing notifications while it is on)", false)
	}
	jid, err := ChatJID(to)
	if err != nil {
		return err
	}
	if strings.HasSuffix(jid, "@g.us") {
		return apierr.Validation("presence is per contact; groups have none")
	}
	cli, err := s.client(instanceID)
	if err != nil {
		return err
	}
	if !s.presenceQuota.take(instanceID, time.Now()) {
		return apierr.New(http.StatusTooManyRequests, "rate_limited",
			"too many presence subscriptions; at most 20 per minute and 200 per day per instance", true)
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	if _, err := cli.SubscribePresence(rctx, &workerv1.SubscribePresenceRequest{Jid: jid}); err != nil {
		return mapErr(err)
	}
	return nil
}
