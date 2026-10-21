package attendant

import (
	"context"
	"sync"
	"time"
)

// Knowledge sources of the product list (Profile.KnowledgeSource, marco 12).
const (
	KnowledgeManual  = "manual"  // the products typed in the profile (default)
	KnowledgeCatalog = "catalog" // the instance's WhatsApp Business catalog
)

// sourceCatalog marks knowledge entries that came from the WhatsApp catalog: written by
// whoever edits the catalog, they are rendered as untrusted data, outside <business>.
const sourceCatalog = "catalog"

// Catalog refresh timing: a good list is refreshed after catalogRefresh, a failure is
// retried after catalogRetry, and a first load waits at most catalogColdWait (later
// answers never wait: refreshes run in the background).
const (
	catalogRefresh   = 10 * time.Minute
	catalogRetry     = 2 * time.Minute
	catalogFetchTime = 20 * time.Second
)

var catalogColdWait = 2 * time.Second // a var: tests shorten it

// CatalogSource lists the products of an instance's WhatsApp catalog as knowledge
// entries (kind product: name, price, description with availability).
type CatalogSource func(ctx context.Context, instanceID string) ([]KnowledgeEntry, error)

type catalogCache struct {
	mu      sync.Mutex
	entries map[string]*catalogCached
}

type catalogCached struct {
	ok       bool // entries holds a good list
	at       time.Time
	failedAt time.Time
	entries  []KnowledgeEntry
	loading  chan struct{} // closed when the fetch in progress ends (nil: none)
}

// catalogList returns the instance's cached catalog products, starting a background
// refresh when the list is old (or the last failure is old enough to retry). Only a cold
// start (no list yet) waits, at most catalogColdWait.
func (a *Attendant) catalogList(ctx context.Context, instanceID string) ([]KnowledgeEntry, bool) {
	now := a.now()
	a.catalog.mu.Lock()
	if a.catalog.entries == nil {
		a.catalog.entries = map[string]*catalogCached{}
	}
	c := a.catalog.entries[instanceID]
	if c == nil {
		c = &catalogCached{}
		a.catalog.entries[instanceID] = c
	}
	stale := !c.ok || now.Sub(c.at) >= catalogRefresh
	retryable := c.failedAt.IsZero() || now.Sub(c.failedAt) >= catalogRetry
	if stale && retryable && c.loading == nil {
		done := make(chan struct{})
		c.loading = done
		go a.fetchCatalog(instanceID, c, done)
	}
	loading, cold := c.loading, !c.ok
	a.catalog.mu.Unlock()
	if cold && loading != nil {
		t := time.NewTimer(catalogColdWait)
		select {
		case <-loading:
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
	}
	a.catalog.mu.Lock()
	defer a.catalog.mu.Unlock()
	return c.entries, c.ok
}

func (a *Attendant) fetchCatalog(instanceID string, c *catalogCached, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), catalogFetchTime)
	list, err := a.Catalog(ctx, instanceID)
	cancel()
	a.catalog.mu.Lock()
	if err == nil {
		c.ok, c.at, c.failedAt, c.entries = true, a.now(), time.Time{}, safeCatalog(list)
	} else {
		c.failedAt = a.now()
	}
	c.loading = nil
	a.catalog.mu.Unlock()
	close(done)
	if err != nil {
		a.log().Warn("attendant: catalog products unavailable, keeping the last known list", "instance_id", instanceID, "err", err)
	}
}

// safeCatalog neutralizes catalog text (third-party content) and marks the entries.
func safeCatalog(list []KnowledgeEntry) []KnowledgeEntry {
	out := make([]KnowledgeEntry, 0, len(list))
	for _, k := range list {
		k.Kind, k.Source = "product", sourceCatalog
		k.Title, k.Content = clip(neutralize(k.Title), maxTitleLen), clip(neutralize(k.Content), maxContentLen)
		if k.Title == "" {
			continue
		}
		out = append(out, k)
	}
	return out
}

// withCatalog replaces the profile's manual products with the WhatsApp catalog when the
// profile asks for it. Without a good list yet (first load still running, or failing),
// the manual products stay: an answer never fails because of the catalog.
func (a *Attendant) withCatalog(ctx context.Context, instanceID string, p Profile) Profile {
	if p.KnowledgeSource != KnowledgeCatalog || a.Catalog == nil {
		return p
	}
	list, ok := a.catalogList(ctx, instanceID)
	if !ok {
		return p
	}
	kept := make([]KnowledgeEntry, 0, len(p.Knowledge)+len(list))
	for _, k := range p.Knowledge {
		if k.Kind != "product" {
			kept = append(kept, k)
		}
	}
	p.Knowledge = append(kept, list...)
	return p
}
