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

// Presence subscriptions per instance per minute: each one asks WhatsApp for a contact's
// online status, and a burst of them looks like scraping.
const presenceSubsPerMinute = 20

// presenceQuota is a sliding one-minute window of subscriptions per instance.
type presenceQuota struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func (q *presenceQuota) take(instanceID string, now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.hits == nil {
		q.hits = map[string][]time.Time{}
	}
	var keep []time.Time
	for _, t := range q.hits[instanceID] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= presenceSubsPerMinute {
		q.hits[instanceID] = keep
		return false
	}
	q.hits[instanceID] = append(keep, now)
	return true
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
			"too many presence subscriptions; at most 20 per minute per instance", true)
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	if _, err := cli.SubscribePresence(rctx, &workerv1.SubscribePresenceRequest{Jid: jid}); err != nil {
		return mapErr(err)
	}
	return nil
}
