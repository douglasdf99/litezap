package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMenuItemPhotos(t *testing.T) {
	st := ordersStore(t)
	ctx := context.Background()
	it, err := st.CreateMenuItem(ctx, "i1", MenuItem{Name: "Pizza", Available: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxMenuImages; i++ {
		if err := st.AddMenuItemImage(ctx, "i1", it.ID, fmt.Sprintf("mimg_%016d", i), fmt.Sprintf("https://x/%d.jpg", i), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddMenuItemImage(ctx, "i1", it.ID, "mimg_over", "", false); !errors.Is(err, ErrMenuLimit) {
		t.Fatalf("11th photo = %v", err)
	}
	got, _ := st.GetMenuItem(ctx, "i1", it.ID)
	if len(got.Images) != MaxMenuImages || got.ImageID != "mimg_0000000000000000" || got.Images[3].SourceURL != "https://x/3.jpg" {
		t.Fatalf("photos = %+v main %s", got.Images, got.ImageID)
	}
	// The main photo is the first one; making another one main moves it first.
	if err := st.SetMenuItemMainImage(ctx, "i1", it.ID, "mimg_0000000000000005"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetMenuItem(ctx, "i1", it.ID)
	if got.ImageID != "mimg_0000000000000005" || got.Images[0].ID != got.ImageID || got.Images[1].ID != "mimg_0000000000000000" {
		t.Fatalf("after main = %+v", got.Images)
	}
	// A reorder must list every photo exactly once.
	if err := st.ReorderMenuItemImages(ctx, "i1", it.ID, []string{"mimg_0000000000000001"}); !errors.Is(err, ErrMenuOrder) {
		t.Fatalf("partial reorder = %v", err)
	}
	ids := []string{}
	for i := MaxMenuImages - 1; i >= 0; i-- {
		ids = append(ids, fmt.Sprintf("mimg_%016d", i))
	}
	if err := st.ReorderMenuItemImages(ctx, "i1", it.ID, ids); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetMenuItem(ctx, "i1", it.ID)
	if got.ImageID != "mimg_0000000000000009" {
		t.Fatalf("after reorder main = %s", got.ImageID)
	}
	// A collection cover is cleared when its photo goes.
	c, err := st.CreateMenuCollection(ctx, "i1", MenuCollection{Name: "Promoções", Active: true, CoverImageID: "mimg_0000000000000009"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMenuCollection(ctx, "i1", MenuCollection{Name: "X", CoverImageID: "mimg_nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown cover = %v", err)
	}
	if err := st.RemoveMenuItemImage(ctx, "i1", it.ID, "mimg_0000000000000009"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetMenuItem(ctx, "i1", it.ID)
	if got.ImageID != "mimg_0000000000000008" || len(got.Images) != MaxMenuImages-1 || got.Images[0].Position != 0 {
		t.Fatalf("after remove = %s %+v", got.ImageID, got.Images)
	}
	if cc, _ := st.GetMenuCollection(ctx, "i1", c.ID); cc.CoverImageID != "" {
		t.Fatalf("cover kept = %q", cc.CoverImageID)
	}
	// The single-photo API replaces the main photo and keeps the others.
	prev, err := st.SetMenuItemImage(ctx, "i1", it.ID, "mimg_new", "")
	if err != nil || prev != "mimg_0000000000000008" {
		t.Fatal(prev, err)
	}
	got, _ = st.GetMenuItem(ctx, "i1", it.ID)
	if got.ImageID != "mimg_new" || len(got.Images) != MaxMenuImages-1 {
		t.Fatalf("after replace main = %s %d", got.ImageID, len(got.Images))
	}
	used, _ := st.MenuImageIDs(ctx, "i1")
	if !used["mimg_new"] || used["mimg_0000000000000008"] || len(used) != MaxMenuImages-1 {
		t.Fatalf("used = %v", used)
	}
	imgs, err := st.DeleteMenuItem(ctx, "i1", it.ID)
	if err != nil || len(imgs) != MaxMenuImages-1 {
		t.Fatalf("delete returned %v %v", imgs, err)
	}
	if used, _ := st.MenuImageIDs(ctx, "i1"); len(used) != 0 {
		t.Fatalf("photos left %v", used)
	}
	// Another instance cannot touch these photos.
	other, _ := st.CreateMenuItem(ctx, "i1", MenuItem{Name: "B"})
	if err := st.AddMenuItemImage(ctx, "i2", other.ID, "mimg_x", "", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other instance photo = %v", err)
	}
}

func TestMenuCollections(t *testing.T) {
	st := ordersStore(t)
	ctx := context.Background()
	a, _ := st.CreateMenuItem(ctx, "i1", MenuItem{Name: "A", Available: true})
	b, _ := st.CreateMenuItem(ctx, "i1", MenuItem{Name: "B", Available: true})
	promo, err := st.CreateMenuCollection(ctx, "i1", MenuCollection{Name: "Promoções", Description: "Só hoje", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	best, _ := st.CreateMenuCollection(ctx, "i1", MenuCollection{Name: "Mais vendidos", Active: true})
	if err := st.AddMenuCollectionItems(ctx, "i1", promo.ID, []string{a.ID, b.ID, a.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddMenuCollectionItems(ctx, "i1", promo.ID, []string{"mitm_nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown item = %v", err)
	}
	if err := st.AddMenuCollectionItems(ctx, "i1", best.ID, []string{b.ID}); err != nil {
		t.Fatal(err)
	}
	c, _ := st.GetMenuCollection(ctx, "i1", promo.ID)
	if len(c.ItemIDs) != 2 || c.ItemIDs[0] != a.ID {
		t.Fatalf("items = %v", c.ItemIDs)
	}
	if err := st.ReorderMenuCollectionItems(ctx, "i1", promo.ID, []string{b.ID, a.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReorderMenuCollectionItems(ctx, "i1", promo.ID, []string{b.ID}); !errors.Is(err, ErrMenuOrder) {
		t.Fatalf("partial reorder = %v", err)
	}
	got, _ := st.GetMenuItem(ctx, "i1", b.ID)
	if len(got.CollectionIDs) != 2 || got.CollectionIDs[0] != promo.ID {
		t.Fatalf("b collections = %v", got.CollectionIDs)
	}
	// UpdateMenuItem with CollectionIDs sets exactly those memberships.
	got.CollectionIDs = []string{best.ID}
	if _, err := st.UpdateMenuItem(ctx, "i1", got); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.GetMenuCollection(ctx, "i1", promo.ID); len(c.ItemIDs) != 1 || c.ItemIDs[0] != a.ID {
		t.Fatalf("promo after update = %v", c.ItemIDs)
	}
	if err := st.ReorderMenuCollections(ctx, "i1", []string{best.ID, promo.ID}); err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListMenuCollections(ctx, "i1")
	if len(list) != 2 || list[0].ID != best.ID || len(list[1].ItemIDs) != 1 {
		t.Fatalf("list = %+v", list)
	}
	// Deleting an item takes it out of its collections; deleting a collection keeps items.
	if _, err := st.DeleteMenuItem(ctx, "i1", a.ID); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.GetMenuCollection(ctx, "i1", promo.ID); len(c.ItemIDs) != 0 {
		t.Fatalf("deleted item kept in collection: %v", c.ItemIDs)
	}
	if err := st.DeleteMenuCollection(ctx, "i1", best.ID); err != nil {
		t.Fatal(err)
	}
	if it, err := st.GetMenuItem(ctx, "i1", b.ID); err != nil || len(it.CollectionIDs) != 0 {
		t.Fatalf("item after collection delete = %+v %v", it, err)
	}
	if _, err := st.GetMenuCollection(ctx, "i2", promo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other instance sees the collection: %v", err)
	}
	// An import can name collections: missing ones are created, matched ignoring case.
	res, err := st.ImportMenu(ctx, "i1", false, []MenuImportItem{{Collections: []string{"promoções", "Novidades"}, Item: MenuItem{Name: "C"}}})
	if err != nil || res.CollectionsCreated != 1 {
		t.Fatalf("import = %+v %v", res, err)
	}
	if c, _ := st.GetMenuCollection(ctx, "i1", promo.ID); len(c.ItemIDs) != 1 {
		t.Fatalf("import did not join the existing collection: %v", c.ItemIDs)
	}
	// A replacing import empties the collections too.
	if _, err := st.ImportMenu(ctx, "i1", true, []MenuImportItem{{Item: MenuItem{Name: "D"}}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.MenuCollectionCount(ctx, "i1"); n != 0 {
		t.Fatalf("collections after replace = %d", n)
	}
}

// The limits hold under concurrency: collections per menu, items per collection and
// photos per item.
func TestMenuCatalogLimitsConcurrent(t *testing.T) {
	st := ordersStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < MaxMenuCollections+10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = st.CreateMenuCollection(ctx, "i1", MenuCollection{Name: fmt.Sprint("c", i), Active: true})
		}(i)
	}
	wg.Wait()
	if n, _ := st.MenuCollectionCount(ctx, "i1"); n != MaxMenuCollections {
		t.Fatalf("collections = %d", n)
	}
	list, _ := st.ListMenuCollections(ctx, "i1")
	col := list[0].ID
	ids := make([]string, 0, MaxCollectionItems+20)
	for i := 0; i < MaxCollectionItems+20; i++ {
		it, err := st.CreateMenuItem(ctx, "i1", MenuItem{Name: fmt.Sprint("x", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, it.ID)
	}
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = st.AddMenuCollectionItems(ctx, "i1", col, []string{id})
		}(id)
	}
	wg.Wait()
	if c, _ := st.GetMenuCollection(ctx, "i1", col); len(c.ItemIDs) != MaxCollectionItems {
		t.Fatalf("collection items = %d", len(c.ItemIDs))
	}
	// All or nothing: a batch over the limit adds nothing.
	if err := st.AddMenuCollectionItems(ctx, "i1", list[1].ID, ids); !errors.Is(err, ErrMenuLimit) {
		t.Fatalf("batch over the limit = %v", err)
	}
	if c, _ := st.GetMenuCollection(ctx, "i1", list[1].ID); len(c.ItemIDs) != 0 {
		t.Fatalf("partial batch kept %d", len(c.ItemIDs))
	}
	for i := 0; i < MaxMenuImages+8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = st.AddMenuItemImage(ctx, "i1", ids[0], fmt.Sprintf("mimg_%016d", i), "", false)
		}(i)
	}
	wg.Wait()
	if it, _ := st.GetMenuItem(ctx, "i1", ids[0]); len(it.Images) != MaxMenuImages || it.ImageID != it.Images[0].ID {
		t.Fatalf("photos = %d main %s", len(it.Images), it.ImageID)
	}
}

func TestMenuBulkAndDuplicate(t *testing.T) {
	st := ordersStore(t)
	ctx := context.Background()
	cat, _ := st.CreateMenuCategory(ctx, "i1", "Pizzas")
	opts := []MenuOption{{Name: "Tamanho", Min: 1, Max: 1, Required: true, Choices: []MenuChoice{{Name: "P"}, {Name: "G", PriceDeltaCents: 1000}}}}
	a, _ := st.CreateMenuItem(ctx, "i1", MenuItem{CategoryID: cat.ID, Name: "A", PriceCents: 1000, SKU: "A-1", Available: true, Options: opts,
		SalePriceCents: 900, SaleUntil: "2030-01-01", LinkURL: "https://x/a", Tags: []string{"t"}})
	b, _ := st.CreateMenuItem(ctx, "i1", MenuItem{Name: "B", PriceCents: 2000, Available: true})
	// An unknown item makes the whole bulk change nothing.
	err := st.UpdateMenuItems(ctx, "i1", []string{a.ID, "mitm_nope"}, func(it *MenuItem) error { it.Hidden = true; return nil })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("bulk with an unknown item = %v", err)
	}
	if it, _ := st.GetMenuItem(ctx, "i1", a.ID); it.Hidden {
		t.Fatal("partial bulk saved")
	}
	if err := st.UpdateMenuItems(ctx, "i1", []string{a.ID, b.ID}, func(it *MenuItem) error {
		it.PriceCents += 100
		it.CategoryID = cat.ID
		it.Available = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if it, _ := st.GetMenuItem(ctx, "i1", b.ID); it.PriceCents != 2100 || it.CategoryID != cat.ID || it.Available || it.Position != 1 {
		t.Fatalf("bulk b = %+v", it)
	}
	if err := st.UpdateMenuItems(ctx, "i1", []string{b.ID}, func(it *MenuItem) error { it.CategoryID = "mcat_nope"; return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown category = %v", err)
	}
	// Duplicate: fields, options, collections and the given photos; no sku.
	col, _ := st.CreateMenuCollection(ctx, "i1", MenuCollection{Name: "Promo", Active: true})
	_ = st.AddMenuCollectionItems(ctx, "i1", col.ID, []string{a.ID})
	_ = st.AddMenuItemImage(ctx, "i1", a.ID, "mimg_a1", "https://x/1.jpg", false)
	_ = st.AddMenuItemImage(ctx, "i1", a.ID, "mimg_a2", "", false)
	cp, err := st.DuplicateMenuItem(ctx, "i1", a.ID, "A grande", map[string]string{"mimg_a1": "mimg_c1", "mimg_a2": "mimg_c2"})
	if err != nil {
		t.Fatal(err)
	}
	if cp.ID == a.ID || cp.Name != "A grande" || cp.SKU != "" || cp.PriceCents != 1100 || cp.SalePriceCents != 900 || cp.LinkURL != "https://x/a" ||
		len(cp.Options) != 1 || cp.Options[0].ID == a.Options[0].ID || len(cp.CollectionIDs) != 1 || cp.ImageID != "mimg_c1" ||
		len(cp.Images) != 2 || cp.Images[0].SourceURL != "https://x/1.jpg" || cp.CategoryID != cat.ID {
		t.Fatalf("copy = %+v", cp)
	}
	if orig, _ := st.GetMenuItem(ctx, "i1", a.ID); orig.ImageID != "mimg_a1" || len(orig.Images) != 2 {
		t.Fatalf("original changed: %+v", orig)
	}
	imgs, err := st.DeleteMenuItems(ctx, "i1", []string{a.ID, cp.ID})
	if err != nil || len(imgs) != 4 {
		t.Fatalf("bulk delete = %v %v", imgs, err)
	}
}

func TestOrderDrafts(t *testing.T) {
	st := ordersStore(t)
	ctx := context.Background()
	d, err := st.CreateOrderDraft(ctx, "i1", OrderDraftRow{Chat: "5511@s.whatsapp.net", Items: json.RawMessage(`[{"name":"x"}]`),
		SubtotalCents: 1000, TotalCents: 1000, Valid: true, Source: "mcp"})
	if err != nil || d.Status != OrderDraft || d.ID == "" || d.Currency != "BRL" {
		t.Fatal(d, err)
	}
	d.TotalCents, d.Items = 2000, json.RawMessage(`[{"name":"y"}]`)
	if got, err := st.ReplaceOrderDraftItems(ctx, "i1", d); err != nil || got.TotalCents != 2000 {
		t.Fatal(got, err)
	}
	if _, err := st.SetOrderDraftStatus(ctx, "i1", d.ID, OrderSent, "msg_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReplaceOrderDraftItems(ctx, "i1", d); !errors.Is(err, ErrOrderStatus) {
		t.Fatalf("items of a sent draft changed: %v", err)
	}
	got, _ := st.SetOrderDraftStatus(ctx, "i1", d.ID, OrderConfirmed, "")
	if got.Status != OrderConfirmed || got.MessageID != "msg_1" {
		t.Fatalf("confirmed = %+v", got)
	}
	if _, err := st.SetOrderDraftStatus(ctx, "i1", d.ID, OrderDraft, ""); !errors.Is(err, ErrOrderStatus) {
		t.Fatalf("confirmed back to draft = %v", err)
	}
	if _, err := st.SetOrderDraftStatus(ctx, "i1", d.ID, OrderCancelled, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetOrderDraftStatus(ctx, "i1", d.ID, OrderSent, ""); !errors.Is(err, ErrOrderStatus) {
		t.Fatalf("a cancelled draft moved: %v", err)
	}
	if _, err := st.GetOrderDraft(ctx, "i2", d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other instance reads the draft: %v", err)
	}
	// The open draft of a chat (attendant updates it).
	open, _ := st.CreateOrderDraft(ctx, "i1", OrderDraftRow{Chat: "5511@s.whatsapp.net", Source: "attendant", Valid: true})
	if got, err := st.OpenOrderDraft(ctx, "i1", "5511@s.whatsapp.net", "attendant", time.Now().Add(-time.Hour)); err != nil || got.ID != open.ID {
		t.Fatalf("open draft = %+v %v", got, err)
	}
	if _, err := st.OpenOrderDraft(ctx, "i1", "5511@s.whatsapp.net", "attendant", time.Now().Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an old draft is not open: %v", err)
	}
	list, _ := st.ListOrderDrafts(ctx, "i1", OrderDraftFilter{Status: OrderCancelled})
	if len(list) != 1 || list[0].ID != d.ID {
		t.Fatalf("filtered list = %+v", list)
	}
	if list, _ := st.ListOrderDrafts(ctx, "i1", OrderDraftFilter{Source: "attendant"}); len(list) != 1 {
		t.Fatalf("by source = %+v", list)
	}
	// The oldest drafts beyond MaxOrderDrafts are removed.
	for i := 0; i < MaxOrderDrafts; i++ {
		if _, err := st.CreateOrderDraft(ctx, "i1", OrderDraftRow{Source: "api"}); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := st.ListOrderDrafts(ctx, "i1", OrderDraftFilter{Limit: 200})
	var n int
	_ = st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM order_drafts WHERE instance_id = 'i1'`).Scan(&n)
	if n != MaxOrderDrafts || len(all) != 200 {
		t.Fatalf("drafts kept = %d", n)
	}
	if _, err := st.GetOrderDraft(ctx, "i1", d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("the oldest draft was kept")
	}
}
