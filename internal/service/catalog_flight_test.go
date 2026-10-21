package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/catalog/catalogtest"
)

// Concurrent cache misses reach Meta once; images are cached by instance and product.
func TestCatalogSharedMissesAndImageCache(t *testing.T) {
	e := newSocialEnv(t)
	f := withCatalog(t, e)
	ctx := context.Background()
	f.Catalogs[ownJID] = []catalogtest.Product{{ID: "101", Name: "Camiseta", Price: 59900, Currency: "BRL"}}
	f.Hold = make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.s.Catalog(ctx, "i1", "", 0, "")
			errs <- err
		}()
	}
	time.Sleep(100 * time.Millisecond) // let every caller queue on the flight
	close(f.Hold)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := f.Calls(); n != 1 {
		t.Fatalf("upstream calls = %d, want 1", n)
	}

	for i := 0; i < 3; i++ {
		if _, _, err := e.s.CatalogProductImage(ctx, "i1", "", "101"); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.ImageCount(); n != 1 {
		t.Fatalf("image downloads = %d, want 1", n)
	}
	e.svc.Catalog.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	if _, _, err := e.s.CatalogProductImage(ctx, "i1", "", "101"); err != nil || f.ImageCount() != 2 {
		t.Fatalf("expired image: %v, downloads %d", err, f.ImageCount())
	}
}

func TestImageCacheLRU(t *testing.T) {
	var c imageCache
	now := time.Now()
	for i := 0; i < imageCacheEntries+5; i++ {
		c.put(string(rune('a'+i%26))+string(rune('0'+i/26)), cachedImage{data: []byte("x"), at: now})
	}
	if len(c.items) != imageCacheEntries || len(c.order) != imageCacheEntries {
		t.Fatalf("entries = %d/%d", len(c.items), len(c.order))
	}
	c.put("big", cachedImage{data: make([]byte, imageCacheBytes), at: now})
	if _, ok := c.get("big", now); ok {
		t.Fatal("an oversized image must not be cached")
	}
}
