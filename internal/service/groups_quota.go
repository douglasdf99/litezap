package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
)

// addGap is the minimum time between two batches of participant adds of an instance.
const addGap = 10 * time.Second

type quotaKind int

const (
	quotaAdd quotaKind = iota
	quotaCreate
	quotaJoin
)

// quotaState is what the group quotas remember per instance (engine_kv, so a restart
// does not reset the day): one timestamp (unix ns) per participant added, group created
// and group joined in the last 24h.
type quotaState struct {
	Adds    []int64 `json:"adds,omitempty"`
	Creates []int64 `json:"creates,omitempty"`
	Joins   []int64 `json:"joins,omitempty"`
	LastAdd int64   `json:"last_add,omitempty"`
}

// reservation is what reserve counted, so a request that never reached WhatsApp can
// give it back.
type reservation struct {
	kind  quotaKind
	at    int64
	count int // creates or joins
	adds  int
	prev  int64 // LastAdd before the reservation
}

type groupQuota struct {
	mu sync.Mutex // serializes read-check-write of every instance's state
}

func quotaKey(instanceID string) string { return "groups.quota:" + instanceID }

func loadQuota(ctx context.Context, st *store.Store, instanceID string) (quotaState, error) {
	var q quotaState
	raw, err := st.GetKV(ctx, quotaKey(instanceID))
	if errors.Is(err, store.ErrNotFound) {
		return q, nil
	}
	if err != nil {
		return q, err
	}
	if json.Unmarshal([]byte(raw), &q) != nil {
		return quotaState{}, nil // a corrupt entry starts over rather than blocking groups
	}
	return q, nil
}

func saveQuota(ctx context.Context, st *store.Store, instanceID string, q quotaState) error {
	b, _ := json.Marshal(q)
	return st.SetKV(ctx, quotaKey(instanceID), string(b))
}

func prune(ts []int64, since int64) []int64 {
	return slices.DeleteFunc(ts, func(t int64) bool { return t < since })
}

func countSince(ts []int64, since int64) int {
	n := 0
	for _, t := range ts {
		if t >= since {
			n++
		}
	}
	return n
}

func rateLimited(msg string) error {
	return apierr.New(http.StatusTooManyRequests, "rate_limited", msg, true)
}

// evaluate prunes q to the last 24h and checks count creates/joins (kind) plus adds
// participants against the limits. Any batch with participants is paced (addGap since
// the last one), whatever its kind.
func evaluate(q *quotaState, now time.Time, lim settings.Groups, kind quotaKind, count, adds int) error {
	day, hour := now.Add(-24*time.Hour).UnixNano(), now.Add(-time.Hour).UnixNano()
	q.Adds, q.Creates, q.Joins = prune(q.Adds, day), prune(q.Creates, day), prune(q.Joins, day)
	switch kind {
	case quotaCreate:
		if len(q.Creates)+count > lim.MaxCreatesPerDay {
			return rateLimited(fmt.Sprintf("group creation limit reached: at most %d per day per instance (settings.groups.max_creates_per_day)", lim.MaxCreatesPerDay))
		}
	case quotaJoin:
		if len(q.Joins)+count > lim.MaxJoinsPerDay {
			return rateLimited(fmt.Sprintf("group join limit reached: at most %d per day per instance (settings.groups.max_joins_per_day)", lim.MaxJoinsPerDay))
		}
	}
	if adds == 0 {
		return nil
	}
	if q.LastAdd > 0 && now.Sub(time.Unix(0, q.LastAdd)) < addGap {
		return rateLimited(fmt.Sprintf("participant adds are paced: wait %s between batches", addGap))
	}
	if n := countSince(q.Adds, hour); n+adds > lim.MaxAddsPerHour {
		return rateLimited(fmt.Sprintf("participant add limit reached: at most %d per hour per instance (%d left; settings.groups.max_adds_per_hour)",
			lim.MaxAddsPerHour, max(lim.MaxAddsPerHour-n, 0)))
	}
	if len(q.Adds)+adds > lim.MaxAddsPerDay {
		return rateLimited(fmt.Sprintf("participant add limit reached: at most %d per day per instance (%d left; settings.groups.max_adds_per_day)",
			lim.MaxAddsPerDay, max(lim.MaxAddsPerDay-len(q.Adds), 0)))
	}
	return nil
}

// check is reserve without counting anything: it runs before the participant lookups,
// so a request that the quotas would refuse spends no lookup.
func (gq *groupQuota) check(ctx context.Context, st *store.Store, instanceID string, now time.Time, lim settings.Groups,
	kind quotaKind, count, adds int) error {
	gq.mu.Lock()
	defer gq.mu.Unlock()
	q, err := loadQuota(ctx, st, instanceID)
	if err != nil {
		return err
	}
	return evaluate(&q, now, lim, kind, count, adds)
}

// reserve checks the instance's quotas for count creates/joins (kind) plus adds
// participants, and counts them when they fit; otherwise it answers rate_limited.
func (gq *groupQuota) reserve(ctx context.Context, st *store.Store, instanceID string, now time.Time, lim settings.Groups,
	kind quotaKind, count, adds int) (reservation, error) {
	gq.mu.Lock()
	defer gq.mu.Unlock()
	q, err := loadQuota(ctx, st, instanceID)
	if err != nil {
		return reservation{}, err
	}
	if err := evaluate(&q, now, lim, kind, count, adds); err != nil {
		return reservation{}, err
	}
	at := now.UnixNano()
	r := reservation{kind: kind, at: at, count: count, adds: adds, prev: q.LastAdd}
	for range adds {
		q.Adds = append(q.Adds, at)
	}
	if adds > 0 {
		q.LastAdd = at
	}
	switch kind {
	case quotaCreate:
		for range count {
			q.Creates = append(q.Creates, at)
		}
	case quotaJoin:
		for range count {
			q.Joins = append(q.Joins, at)
		}
	}
	if err := saveQuota(ctx, st, instanceID, q); err != nil {
		return reservation{}, err
	}
	return r, nil
}

// cooldown restarts the pacing gap after WhatsApp itself rate-limited the instance.
func (gq *groupQuota) cooldown(ctx context.Context, st *store.Store, instanceID string, now time.Time) {
	gq.mu.Lock()
	defer gq.mu.Unlock()
	q, err := loadQuota(ctx, st, instanceID)
	if err != nil {
		return
	}
	q.LastAdd = now.UnixNano()
	_ = saveQuota(context.WithoutCancel(ctx), st, instanceID, q)
}

// refund gives back a reservation whose request certainly never reached WhatsApp (see
// notSent). Best effort.
func (gq *groupQuota) refund(ctx context.Context, st *store.Store, instanceID string, r reservation) {
	if r.at == 0 {
		return
	}
	gq.mu.Lock()
	defer gq.mu.Unlock()
	q, err := loadQuota(ctx, st, instanceID)
	if err != nil {
		return
	}
	drop := func(ts []int64, n int) []int64 {
		for i := len(ts) - 1; i >= 0 && n > 0; i-- {
			if ts[i] == r.at {
				ts = slices.Delete(ts, i, i+1)
				n--
			}
		}
		return ts
	}
	q.Adds = drop(q.Adds, r.adds)
	switch r.kind {
	case quotaCreate:
		q.Creates = drop(q.Creates, r.count)
	case quotaJoin:
		q.Joins = drop(q.Joins, r.count)
	}
	if r.adds > 0 && q.LastAdd == r.at {
		q.LastAdd = r.prev
	}
	_ = saveQuota(context.WithoutCancel(ctx), st, instanceID, q)
}
