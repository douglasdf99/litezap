package service

import (
	"context"
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/catalog/catalogtest"
	"github.com/douglasdf99/litezap/internal/menu"
)

// A bulk change only writes its own field, on the item as it is when saving: an edit
// made after the preview (hiding the item) is kept.
func TestMenuBulkKeepsConcurrentEdits(t *testing.T) {
	e := newMsgEnv(t)
	m := newMenu(e)
	ctx := context.Background()
	it, _ := m.CreateItem(ctx, "i1", MenuItemInput{Name: sptr("Pizza"), PriceCents: i64(5000)})
	pct := 10.0
	if _, err := m.Bulk(ctx, "i1", MenuBulkInput{ItemIDs: []string{it.ID}, Action: BulkAdjustPrice, Percent: &pct, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetHidden(ctx, "i1", it.ID, true); err != nil { // between the preview and the save
		t.Fatal(err)
	}
	if _, err := m.Bulk(ctx, "i1", MenuBulkInput{ItemIDs: []string{it.ID}, Action: BulkAdjustPrice, Percent: &pct}); err != nil {
		t.Fatal(err)
	}
	got, _ := m.Item(ctx, "i1", it.ID)
	if !got.Hidden || got.PriceCents != 5500 {
		t.Fatalf("after bulk = hidden %v price %d", got.Hidden, got.PriceCents)
	}
	if _, err := m.Bulk(ctx, "i1", MenuBulkInput{ItemIDs: []string{it.ID}, Action: BulkSetAvailable, Available: bptr(false)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Item(ctx, "i1", it.ID); !got.Hidden || got.Available || got.PriceCents != 5500 {
		t.Fatalf("set_available changed other fields: %+v", got)
	}
}

// A stored draft is only sent while it still matches the menu.
func TestMenuOrderSummaryRefusesStaleDrafts(t *testing.T) {
	e := newMsgEnv(t)
	m := newMenu(e)
	ctx := context.Background()
	pz, _ := m.CreateItem(ctx, "i1", MenuItemInput{Name: sptr("Pizza"), PriceCents: i64(5000)})
	d, err := m.CreateOrderDraft(ctx, "i1", OrderDraftInput{Chat: "5511988887777", Items: []menu.OrderLineInput{{ItemID: pz.ID, Quantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	m.UpdateItem(ctx, "i1", pz.ID, MenuItemInput{PriceCents: i64(6000)})
	if _, err := m.SendOrderSummary(ctx, "i1", d.ID, SendOrderInput{}, 0); err == nil || !strings.Contains(err.Error(), "prices changed") {
		t.Fatalf("repriced draft sent: %v", err)
	}
	m.UpdateItem(ctx, "i1", pz.ID, MenuItemInput{PriceCents: i64(5000)})
	m.SetHidden(ctx, "i1", pz.ID, true)
	if _, err := m.SendOrderSummary(ctx, "i1", d.ID, SendOrderInput{}, 0); err == nil || !strings.Contains(err.Error(), "menu changed") {
		t.Fatalf("draft with a hidden item sent: %v", err)
	}
	m.SetHidden(ctx, "i1", pz.ID, false)
	if _, err := m.SendOrderSummary(ctx, "i1", d.ID, SendOrderInput{}, 0); err != nil {
		t.Fatalf("a current draft was refused: %v", err)
	}
}

// Copying the catalog again fetches only the photos the item lacks, never a duplicate;
// the export leaves out the catalog's expiring CDN links.
func TestMenuCatalogRecopyPhotos(t *testing.T) {
	e := newSocialEnv(t)
	f := withCatalog(t, e)
	f.Catalogs[ownJID] = []catalogtest.Product{{ID: "101", Name: "Camiseta", Price: 59900, Currency: "BRL", Photos: 3}}
	m := newMenu(e.msgEnv)
	ctx := context.Background()
	if _, err := m.ImportCatalog(ctx, "i1", MenuCatalogInput{}); err != nil {
		t.Fatal(err)
	}
	all, _ := m.Search(ctx, "i1", MenuSearchInput{})
	it := all.Items[0]
	if len(it.Images) != 3 || it.Images[1].SourceURL != "wa:101/1" {
		t.Fatalf("photos = %+v", it.Images)
	}
	// Nothing missing: a second copy downloads nothing.
	before := f.ImageCount()
	if res, _ := m.ImportCatalog(ctx, "i1", MenuCatalogInput{}); res.ImagesSaved != 0 || f.ImageCount() != before {
		t.Fatalf("second copy fetched %d photos", res.ImagesSaved)
	}
	// One removed: only that one comes back, once.
	m.RemoveImage(ctx, "i1", it.ID, it.Images[1].ID)
	if res, _ := m.ImportCatalog(ctx, "i1", MenuCatalogInput{}); res.ImagesSaved != 1 {
		t.Fatalf("recopy saved %d", res.ImagesSaved)
	}
	got, _ := m.Item(ctx, "i1", it.ID)
	seen := map[string]bool{}
	for _, im := range got.Images {
		if seen[im.SourceURL] {
			t.Fatalf("duplicate photo %s in %+v", im.SourceURL, got.Images)
		}
		seen[im.SourceURL] = true
	}
	if len(got.Images) != 3 {
		t.Fatalf("photos after recopy = %d", len(got.Images))
	}
	if out, _ := m.ExportCSV(ctx, "i1"); strings.Contains(string(out), "wa:101") {
		t.Fatalf("export lists catalog photo markers: %s", out)
	}
}
