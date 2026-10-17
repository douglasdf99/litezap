// Package lookup resolves phone numbers to WhatsApp JIDs (IsOnWhatsApp) with a cache and a
// per-instance rate limit. The send queue uses it before the first message to a number (a
// number without WhatsApp fails as invalid_number, and Brazilian numbers get the JID with
// or without the 9th digit that WhatsApp actually uses); check-numbers uses it too.
package lookup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/store"
)

// ErrRateLimited means the instance looked up too many uncached numbers in the last minute.
var ErrRateLimited = errors.New("too many number lookups; try again in a minute")

// ErrDailyLimit means check-numbers used up the instance's daily quota of uncached lookups
// (UTC day). It is also an ErrRateLimited.
var ErrDailyLimit = fmt.Errorf("%w: daily limit of number checks reached; try again tomorrow", ErrRateLimited)

// ErrTooManyNumbers means one request has more uncached numbers than the purpose's
// per-minute limit, so it could never be served (not retryable).
var ErrTooManyNumbers = errors.New("too many uncached numbers in one request")

// Workers gives access to running workers (implemented by *manager.Manager).
type Workers interface {
	Client(instanceID string) (workerv1.WorkerClient, error)
}

// Purpose selects the rate-limit bucket: check-numbers has its own, smaller quota, so API
// lookups can never starve the send queue.
type Purpose string

const (
	ForSend  Purpose = "send"
	ForCheck Purpose = "check"
)

type Result struct {
	Phone string
	Found bool
	JID   string
}

// Resolver must be created with its exported fields set; the zero limits take defaults.
type Resolver struct {
	Store   *store.Store
	Workers Workers
	Clock   clock.Clock
	// PerMinute bounds uncached lookups per instance for the send queue (default 30) and
	// CheckPerMinute those for check-numbers (default 10): bulk lookups look like contact
	// scraping to WhatsApp.
	PerMinute, CheckPerMinute int
	// CheckPerDay bounds uncached check-numbers lookups per instance and UTC day (default
	// 200). The send queue applies its own daily cap (the ramp's), see queue.
	CheckPerDay int
	// FoundTTL and MissingTTL say how long a result is trusted (defaults 30 days and 24h).
	FoundTTL, MissingTTL time.Duration

	mu     sync.Mutex
	recent map[string][]time.Time
	daily  map[string]int // instance + "/" + UTC date -> uncached check lookups
}

func (r *Resolver) now() time.Time {
	if r.Clock == nil {
		return time.Now()
	}
	return r.Clock.Now()
}

func (r *Resolver) fresh(c store.CachedNumber, now time.Time) bool {
	ttl := r.FoundTTL
	if ttl == 0 {
		ttl = 30 * 24 * time.Hour
	}
	if !c.Found {
		if ttl = r.MissingTTL; ttl == 0 {
			ttl = 24 * time.Hour
		}
	}
	return now.Sub(c.CheckedAt) < ttl
}

// limit is the per-minute quota of purpose.
func (r *Resolver) limit(purpose Purpose) int {
	limit := r.PerMinute
	if limit == 0 {
		limit = 30
	}
	if purpose == ForCheck {
		if limit = r.CheckPerMinute; limit == 0 {
			limit = 10
		}
	}
	return limit
}

// take reserves n lookups in the instance's one-minute window for purpose (and, for
// check-numbers, in its daily quota).
func (r *Resolver) take(instanceID string, purpose Purpose, n int, now time.Time) error {
	limit := r.limit(purpose)
	key := instanceID + "/" + string(purpose)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recent == nil {
		r.recent, r.daily = map[string][]time.Time{}, map[string]int{}
	}
	dayKey := instanceID + "/" + now.UTC().Format(time.DateOnly)
	if purpose == ForCheck {
		perDay := r.CheckPerDay
		if perDay == 0 {
			perDay = 200
		}
		if r.daily[dayKey]+n > perDay {
			return ErrDailyLimit
		}
	}
	keep := r.recent[key][:0]
	for _, t := range r.recent[key] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep)+n > limit {
		r.recent[key] = keep
		return ErrRateLimited
	}
	for i := 0; i < n; i++ {
		keep = append(keep, now)
	}
	r.recent[key] = keep
	if purpose == ForCheck {
		r.daily[dayKey] += n
	}
	return nil
}

// Cached returns the fresh cached result for phone without contacting WhatsApp or
// spending quota; ok=false when there is none.
func (r *Resolver) Cached(ctx context.Context, instanceID, phone string) (res Result, ok bool, err error) {
	c, err := r.Store.GetCachedNumber(ctx, instanceID, phone)
	if errors.Is(err, store.ErrNotFound) {
		return Result{Phone: phone}, false, nil
	}
	if err != nil || !r.fresh(c, r.now()) {
		return Result{Phone: phone}, false, err
	}
	return Result{Phone: phone, Found: c.Found, JID: c.JID}, true, nil
}

// Resolve returns one result per phone (digits with country code), in order. Cached
// results are used while fresh; the rest cost one worker call and count against the
// purpose's limit.
func (r *Resolver) Resolve(ctx context.Context, instanceID string, phones []string, purpose Purpose) ([]Result, error) {
	res, _, err := r.Lookup(ctx, instanceID, phones, purpose)
	return res, err
}

// Lookup is Resolve that also reports whether WhatsApp was asked (network=true: some
// number was not in the cache and the worker was called, successfully or not).
func (r *Resolver) Lookup(ctx context.Context, instanceID string, phones []string, purpose Purpose) (res []Result, network bool, err error) {
	now := r.now()
	out := make([]Result, len(phones))
	var missing []string
	idx := map[string][]int{}
	for i, p := range phones {
		out[i].Phone = p
		c, err := r.Store.GetCachedNumber(ctx, instanceID, p)
		if err == nil && r.fresh(c, now) {
			out[i].Found, out[i].JID = c.Found, c.JID
			continue
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, false, err
		}
		if _, dup := idx[p]; !dup {
			missing = append(missing, p)
		}
		idx[p] = append(idx[p], i)
	}
	if len(missing) == 0 {
		return out, false, nil
	}
	if len(missing) > r.limit(purpose) {
		return nil, false, ErrTooManyNumbers
	}
	// The worker first: a lookup that cannot run spends no quota.
	cli, err := r.Workers.Client(instanceID)
	if err != nil {
		return nil, false, err
	}
	if err := r.take(instanceID, purpose, len(missing), now); err != nil {
		return nil, false, err
	}
	resp, err := cli.CheckNumbers(ctx, &workerv1.CheckNumbersRequest{Phones: missing})
	if err != nil {
		return nil, true, err
	}
	for _, ns := range resp.Results {
		c := store.CachedNumber{Phone: ns.Phone, Found: ns.Exists, JID: ns.Jid, CheckedAt: now}
		if err := r.Store.PutCachedNumber(ctx, instanceID, c); err != nil {
			return nil, true, err
		}
		// The canonical number (e.g. with the 9th digit) resolves to itself: cache it too,
		// so the retargeted send does not cost a second lookup.
		if user, ok := strings.CutSuffix(ns.Jid, "@s.whatsapp.net"); ns.Exists && ok && user != ns.Phone {
			c.Phone = user
			if err := r.Store.PutCachedNumber(ctx, instanceID, c); err != nil {
				return nil, true, err
			}
		}
		for _, i := range idx[ns.Phone] {
			out[i].Found, out[i].JID = ns.Exists, ns.Jid
		}
	}
	return out, true, nil
}
