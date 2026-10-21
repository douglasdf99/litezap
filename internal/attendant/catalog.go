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

// catalogRefresh is how long the catalog products stay in the attendant's cache.
const catalogRefresh = 10 * time.Minute

// CatalogSource lists the products of an instance's WhatsApp catalog as knowledge
// entries (kind product: name, price, description with availability).
type CatalogSource func(ctx context.Context, instanceID string) ([]KnowledgeEntry, error)

type catalogCache struct {
	mu      sync.Mutex
	entries map[string]catalogCached
}

type catalogCached struct {
	at      time.Time
	entries []KnowledgeEntry
}

// withCatalog replaces the profile's manual products with the WhatsApp catalog when the
// profile asks for it. On a failure the last good list is kept (or the manual products,
// when there is none): an answer never waits on, nor fails because of, the catalog.
func (a *Attendant) withCatalog(ctx context.Context, instanceID string, p Profile) Profile {
	if p.KnowledgeSource != KnowledgeCatalog || a.Catalog == nil {
		return p
	}
	a.catalog.mu.Lock()
	c, ok := a.catalog.entries[instanceID]
	a.catalog.mu.Unlock()
	if !ok || a.now().Sub(c.at) >= catalogRefresh {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		list, err := a.Catalog(cctx, instanceID)
		cancel()
		switch {
		case err == nil:
			c = catalogCached{at: a.now(), entries: list}
			a.catalog.mu.Lock()
			if a.catalog.entries == nil {
				a.catalog.entries = map[string]catalogCached{}
			}
			a.catalog.entries[instanceID] = c
			a.catalog.mu.Unlock()
			ok = true
		default:
			a.log().Warn("attendant: catalog products unavailable, using the last known list", "instance_id", instanceID, "err", err)
		}
	}
	if !ok {
		return p
	}
	kept := make([]KnowledgeEntry, 0, len(p.Knowledge)+len(c.entries))
	for _, k := range p.Knowledge {
		if k.Kind != "product" {
			kept = append(kept, k)
		}
	}
	p.Knowledge = append(kept, c.entries...)
	return p
}
