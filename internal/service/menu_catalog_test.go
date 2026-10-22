package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/catalog/catalogtest"
	"github.com/douglasdf99/litezap/internal/menu"
	"github.com/douglasdf99/litezap/internal/store"
)

func i64(n int64) *int64 { return &n }
func bptr(b bool) *bool  { return &b }

func photo(t *testing.T) string {
	return base64.StdEncoding.EncodeToString(pngImage(t, 64, 48))
}

func TestMenuSalePriceLinkHidden(t *testing.T) {
	e := newMsgEnv(t)
	m := newMenu(e)
	ctx := context.Background()
	it, err := m.CreateItem(ctx, "i1", MenuItemInput{Name: sptr("Pizza"), Price: json.RawMessage(`"50"`), SalePrice: json.RawMessage(`"39,90"`),
		SaleUntil: sptr("31/12/2099"), LinkURL: sptr("https://loja.example/pizza"), SKU: sptr("PZ-1")})
	if err != nil {
		t.Fatal(err)
	}
	if it.SalePriceCents != 3990 || it.SaleUntil != "2099-12-31" || !it.OnSale || it.EffectivePriceCents != 3990 ||
		it.PriceLabel != "~R$ 50,00~ *R$ 39,90*" || it.LinkURL != "https://loja.example/pizza" || it.SKU != "PZ-1" || it.Hidden {
		t.Fatalf("item = %+v", it)
	}
	// The sale price must stay lower than the price.
	for _, in := range []MenuItemInput{
		{SalePrice: json.RawMessage(`"60"`)},
		{Price: json.RawMessage(`"30"`)},
		{SalePriceCents: i64(5000)},
		{SalePrice: json.RawMessage(`"abc"`)},
		{SaleUntil: sptr("amanhã")},
		{LinkURL: sptr("javascript:alert(1)")},
		{SalePriceCents: i64(100), SalePrice: json.RawMessage(`"1"`)},
	} {
		if _, err := m.UpdateItem(ctx, "i1", it.ID, in); apiCode(err) != "validation_failed" {
			t.Fatalf("%+v accepted: %v", in, err)
		}
	}
	// Removing the sale price removes its date too.
	it, err = m.UpdateItem(ctx, "i1", it.ID, MenuItemInput{SalePrice: json.RawMessage(`""`)})
	if err != nil || it.SalePriceCents != 0 || it.SaleUntil != "" || it.OnSale || it.PriceLabel != "R$ 50,00" {
		t.Fatalf("cleared sale = %+v %v", it, err)
	}
	// An expired sale is not applied.
	it, _ = m.UpdateItem(ctx, "i1", it.ID, MenuItemInput{SalePriceCents: i64(4000), SaleUntil: sptr("2001-01-01")})
	if it.OnSale || it.EffectivePriceCents != 5000 {
		t.Fatalf("expired sale = %+v", it)
	}
	// Hidden: kept in the menu, never sent, never in the menu text.
	it, err = m.SetHidden(ctx, "i1", it.ID, true)
	if err != nil || !it.Hidden {
		t.Fatal(it, err)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "menu_item", MenuItemID: it.ID}, 0); err == nil || !strings.Contains(err.Error(), "hidden") {
		t.Fatalf("hidden item sent: %v", err)
	}
	m.CreateItem(ctx, "i1", MenuItemInput{Name: sptr("Coca"), PriceCents: i64(1200)})
	txt, _ := m.Text(ctx, "i1", MenuTextInput{IncludeUnavailable: true})
	if strings.Contains(strings.Join(txt.Chunks, ""), "Pizza") || !strings.Contains(txt.Chunks[0], "Coca") {
		t.Fatalf("text = %v", txt.Chunks)
	}
	v, _ := m.Get(ctx, "i1")
	if v.ItemCount != 2 { // the management view lists hidden items, flagged
		t.Fatalf("menu = %+v", v)
	}
}

func TestMenuPhotosAndAlbum(t *testing.T) {
	e := newMsgEnv(t)
	m := newMenu(e)
	ctx := context.Background()
	it, _ := m.CreateItem(ctx, "i1", MenuItemInput{Name: sptr("Pizza"), PriceCents: i64(5000)})
	v, err := m.SetImage(ctx, "i1", it.ID, MenuImageInput{Base64: photo(t)}) // the single-photo API: the main photo
	if err != nil || len(v.Images) != 1 || !v.Images[0].Main {
		t.Fatal(v, err)
	}
	first := v.Images[0].ID
	for i := 0; i < store.MaxMenuImages-1; i++ {
		if v, err = m.AddImage(ctx, "i1", it.ID, MenuImageInput{Base64: photo(t)}); err != nil {
			t.Fatal(err)
		}
	}
	if len(v.Images) != store.MaxMenuImages || v.Images[0].ID != first {
		t.Fatalf("photos = %+v", v.Images)
	}
	if _, err := m.AddImage(ctx, "i1", it.ID, MenuImageInput{Base64: photo(t)}); apiCode(err) != "menu_limit" {
		t.Fatalf("11th photo = %v", err)
	}
	second := v.Images[1].ID
	if v, err = m.SetMainImage(ctx, "i1", it.ID, second); err != nil || v.Images[0].ID != second || !strings.Contains(v.ImagePath, second[5:]) {
		t.Fatalf("main = %+v %v", v.Images, err)
	}
	if b, err := m.ImageByID(ctx, "i1", it.ID, first); err != nil || len(b) == 0 {
		t.Fatal(err)
	}
	// Removing a photo removes its file.
	file := filepath.Join(e.dir, "menu", "i1", first+".jpg")
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	if v, err = m.RemoveImage(ctx, "i1", it.ID, first); err != nil || len(v.Images) != store.MaxMenuImages-1 {
		t.Fatal(v, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("photo file kept")
	}
	ids := []string{}
	for i := len(v.Images) - 1; i >= 0; i-- {
		ids = append(ids, v.Images[i].ID)
	}
	if v, err = m.ReorderImages(ctx, "i1", it.ID, ReorderInput{IDs: ids}); err != nil || v.Images[0].ID != ids[0] {
		t.Fatal(v, err)
	}
	// Album: the main photo with the caption, then the others without, in order.
	res, err := m.SendItemAlbum(ctx, "i1", it.ID, SendItemPhotosInput{To: "5511988887777", MaxPhotos: 3, ClientMessageID: "alb"}, 0)
	if err != nil || len(res.Messages) != 3 || res.Messages[2].ClientMessageID != "alb:3" {
		t.Fatalf("album = %+v %v", res, err)
	}
	e.eventually("album sent", func() bool { return len(e.sp.Last("i1").WA.Sent()) >= 3 })
	sent := e.sp.Last("i1").WA.Sent()
	if sent[0].Type != "image" || !strings.HasPrefix(sent[0].Text, "*Pizza*") || sent[1].Type != "image" || sent[1].Text != "" {
		t.Fatalf("album messages = %+v", sent[:3])
	}
	// A photo of another item is refused.
	other, _ := m.CreateItem(ctx, "i1", MenuItemInput{Name: sptr("Outro")})
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "menu_item", MenuItemID: other.ID, MenuImageID: ids[0]}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("foreign photo = %v", err)
	}
	// Deleting the item removes every photo file.
	if err := m.DeleteItem(ctx, "i1", it.ID); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(filepath.Join(e.dir, "menu", "i1"))
	if len(files) != 0 {
		t.Fatalf("files left: %d", len(files))
	}
}

func TestMenuCollectionsService(t *testing.T) {
	e := newMsgEnv(t)
	m := newMenu(e)
	ctx := context.Background()
	a, _ := m.CreateItem(ctx, "i1", MenuItemInput{Name: sptr("Calabresa"), PriceCents: i64(5000), SalePriceCents: i64(3990)})
	b, _ := m.CreateItem(ctx, "i1", MenuItemInput{Name: sptr("Coca"), PriceCents: i64(1200)})
	h, _ := m.CreateItem(ctx, "i1", MenuItemInput{Name: sptr("Secreta"), PriceCents: i64(100), Hidden: bptr(true)})
	m.SetImage(ctx, "i1", a.ID, MenuImageInput{Base64: photo(t)})
	m.SetImage(ctx, "i1", b.ID, MenuImageInput{Base64: photo(t)})
	if _, err := m.CreateCollection(ctx, "i1", MenuCollectionInput{Name: sptr(" ")}); apiCode(err) != "validation_failed" {
		t.Fatalf("empty name = %v", err)
	}
	c, err := m.CreateCollection(ctx, "i1", MenuCollectionInput{Name: sptr("Promoções"), Description: sptr("Só hoje"),
		ItemIDs: &[]string{a.ID, h.ID}, CoverItemID: &a.ID})
	if err != nil || c.ItemCount != 2 || !c.Active || c.CoverImageID == "" || !strings.Contains(c.CoverPath, "/images/") || len(c.Items) != 2 {
		t.Fatalf("collection = %+v %v", c, err)
	}
	if c, err = m.AddCollectionItems(ctx, "i1", c.ID, MenuItemIDsInput{ItemIDs: []string{b.ID}}); err != nil || c.ItemIDs[2] != b.ID {
		t.Fatal(c, err)
	}
	if c, err = m.ReorderCollectionItems(ctx, "i1", c.ID, ReorderInput{IDs: []string{b.ID, a.ID, h.ID}}); err != nil || c.Items[0].ID != b.ID {
		t.Fatal(c, err)
	}
	// Items can join collections through collection_ids too.
	it, err := m.UpdateItem(ctx, "i1", b.ID, MenuItemInput{CollectionIDs: &[]string{}})
	if err != nil || len(it.CollectionIDs) != 0 {
		t.Fatal(it, err)
	}
	if _, err := m.UpdateItem(ctx, "i1", b.ID, MenuItemInput{CollectionIDs: &[]string{"mcol_0000000000000000"}}); apiCode(err) != "not_found" {
		t.Fatalf("unknown collection = %v", err)
	}
	it, _ = m.UpdateItem(ctx, "i1", b.ID, MenuItemInput{CollectionIDs: &[]string{c.ID}})
	if len(it.CollectionIDs) != 1 {
		t.Fatal(it)
	}
	// Text: in order, sale prices, never hidden items.
	txt, err := m.Text(ctx, "i1", MenuTextInput{CollectionID: c.ID})
	if err != nil || len(txt.Chunks) != 1 || !strings.HasPrefix(txt.Chunks[0], "*Promoções*\nSó hoje\n\n• Calabresa — ~R$ 50,00~ *R$ 39,90*\n• Coca") ||
		strings.Contains(txt.Chunks[0], "Secreta") {
		t.Fatalf("text = %+v %v", txt, err)
	}
	// Send: the text, then the photos of the first items (name and price under each).
	res, err := m.SendCollection(ctx, "i1", c.ID, SendCollectionInput{To: "5511988887777", Photos: 5, ClientMessageID: "col"}, 0)
	if err != nil || len(res.Messages) != 3 || res.Messages[1].Type != "image" {
		t.Fatalf("send = %+v %v", res, err)
	}
	e.eventually("collection sent", func() bool { return len(e.sp.Last("i1").WA.Sent()) >= 3 })
	sent := e.sp.Last("i1").WA.Sent()
	if !strings.HasPrefix(sent[0].Text, "*Promoções*") || sent[1].Text != "*Calabresa* — ~R$ 50,00~ *R$ 39,90*" {
		t.Fatalf("sent = %q / %q", sent[0].Text, sent[1].Text)
	}
	if _, err := m.SendCollection(ctx, "i1", c.ID, SendCollectionInput{To: "5511988887777", Photos: 11}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("photos over the cap = %v", err)
	}
	// An inactive collection is not sent.
	if _, err := m.UpdateCollection(ctx, "i1", c.ID, MenuCollectionInput{Active: bptr(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SendCollection(ctx, "i1", c.ID, SendCollectionInput{To: "5511988887777"}, 0); err == nil || !strings.Contains(err.Error(), "inactive") {
		t.Fatalf("inactive collection sent: %v", err)
	}
	list, _ := m.ListCollections(ctx, "i1")
	if len(list) != 1 || list[0].Active {
		t.Fatalf("list = %+v", list)
	}
	if err := m.DeleteCollection(ctx, "i1", c.ID); err != nil {
		t.Fatal(err)
	}
	if v, _ := m.Get(ctx, "i1"); v.ItemCount != 3 || len(v.Collections) != 0 {
		t.Fatalf("after delete = %+v", v)
	}
}

func TestMenuSearchDuplicateBulkExport(t *testing.T) {
	e := newMsgEnv(t)
	m := newMenu(e)
	ctx := context.Background()
	cat, _ := m.CreateCategory(ctx, "i1", MenuCategoryInput{Name: "Pizzas"})
	drinks, _ := m.CreateCategory(ctx, "i1", MenuCategoryInput{Name: "Bebidas"})
	opts := []menu.Option{{Name: "Tamanho", Required: true, Choices: []menu.Choice{{Name: "P"}, {Name: "G", PriceDeltaCents: 1000}}}}
	a, _ := m.CreateItem(ctx, "i1", MenuItemInput{CategoryID: &cat.ID, Name: sptr("Pizza Calabresa"), PriceCents: i64(5000), SalePriceCents: i64(4500),
		Tags: &[]string{"Picante"}, Options: &opts, SKU: sptr("CAL")})
	b, _ := m.CreateItem(ctx, "i1", MenuItemInput{CategoryID: &cat.ID, Name: sptr("Pizza Muçarela"), PriceCents: i64(4000), Available: bptr(false)})
	c, _ := m.CreateItem(ctx, "i1", MenuItemInput{CategoryID: &drinks.ID, Name: sptr("Coca"), PriceCents: i64(1200), Hidden: bptr(true)})
	m.SetImage(ctx, "i1", a.ID, MenuImageInput{Base64: photo(t)})
	col, _ := m.CreateCollection(ctx, "i1", MenuCollectionInput{Name: sptr("Promo"), ItemIDs: &[]string{a.ID}})
	for _, tc := range []struct {
		in   MenuSearchInput
		want int
	}{
		{MenuSearchInput{Query: "pizza"}, 2},
		{MenuSearchInput{Query: "MUCARELA"}, 1},
		{MenuSearchInput{Query: "cal"}, 1}, // sku
		{MenuSearchInput{Tag: "picante"}, 1},
		{MenuSearchInput{CollectionID: col.ID}, 1},
		{MenuSearchInput{CategoryID: &drinks.ID}, 1},
		{MenuSearchInput{Available: bptr(false)}, 1},
		{MenuSearchInput{Hidden: bptr(true)}, 1},
		{MenuSearchInput{OnSale: bptr(true)}, 1},
		{MenuSearchInput{Query: "pizza", Available: bptr(true)}, 1},
		{MenuSearchInput{}, 3},
	} {
		v, err := m.Search(ctx, "i1", tc.in)
		if err != nil || v.Total != tc.want || len(v.Items) != tc.want {
			t.Fatalf("search %+v = %d %v", tc.in, v.Total, err)
		}
	}
	if v, _ := m.Search(ctx, "i1", MenuSearchInput{Limit: 1, Offset: 1}); v.Total != 3 || len(v.Items) != 1 || v.Items[0].ID != b.ID {
		t.Fatalf("page = %+v", v)
	}
	// Duplicate: a variation with its own photo file.
	cp, err := m.DuplicateItem(ctx, "i1", a.ID, MenuDuplicateInput{})
	if err != nil || cp.Name != "Pizza Calabresa (cópia)" || cp.SKU != "" || len(cp.Options) != 1 || len(cp.Images) != 1 ||
		cp.Images[0].ID == a.ID || len(cp.CollectionIDs) != 1 || cp.SalePriceCents != 4500 {
		t.Fatalf("copy = %+v %v", cp, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "menu", "i1", cp.Images[0].ID+".jpg")); err != nil {
		t.Fatal("the copy's photo file is missing")
	}
	cp2, err := m.DuplicateItem(ctx, "i1", a.ID, MenuDuplicateInput{Name: "Calabresa G", CategoryID: &drinks.ID, CopyPhotos: bptr(false)})
	if err != nil || cp2.Name != "Calabresa G" || cp2.CategoryID != drinks.ID || len(cp2.Images) != 0 {
		t.Fatalf("copy 2 = %+v %v", cp2, err)
	}
	// Bulk: a dry run changes nothing and shows before/after.
	pv, err := m.Bulk(ctx, "i1", MenuBulkInput{ItemIDs: []string{a.ID, b.ID}, Action: BulkAdjustPrice, Percent: func() *float64 { f := 10.0; return &f }(), DryRun: true})
	if err != nil || !pv.DryRun || pv.Affected != 2 || pv.Changes[0].NewPriceCents != 5500 || pv.Changes[0].NewSalePriceCents != 4950 ||
		pv.Changes[1].After != "R$ 44,00" {
		t.Fatalf("preview = %+v %v", pv, err)
	}
	if it, _ := m.Item(ctx, "i1", a.ID); it.PriceCents != 5000 {
		t.Fatal("dry run saved")
	}
	// A change that breaks one item refuses the whole save.
	if _, err := m.Bulk(ctx, "i1", MenuBulkInput{ItemIDs: []string{a.ID, b.ID}, Action: BulkAdjustPrice, AmountCents: i64(-4200)}); apiCode(err) != "validation_failed" {
		t.Fatalf("broken bulk = %v", err)
	}
	if it, _ := m.Item(ctx, "i1", b.ID); it.PriceCents != 4000 {
		t.Fatal("a refused bulk saved")
	}
	if _, err := m.Bulk(ctx, "i1", MenuBulkInput{ItemIDs: []string{a.ID, b.ID}, Action: BulkAdjustPrice, AmountCents: i64(-500)}); err != nil {
		t.Fatal(err)
	}
	if it, _ := m.Item(ctx, "i1", a.ID); it.PriceCents != 4500 || it.SalePriceCents != 4000 {
		t.Fatalf("adjusted = %+v", it)
	}
	for _, in := range []MenuBulkInput{
		{ItemIDs: []string{a.ID, b.ID}, Action: BulkSetAvailable, Available: bptr(false)},
		{ItemIDs: []string{a.ID, b.ID}, Action: BulkSetHidden, Hidden: bptr(true)},
		{ItemIDs: []string{a.ID, b.ID}, Action: BulkMoveCategory, CategoryID: &drinks.ID},
		{ItemIDs: []string{a.ID, b.ID}, Action: BulkAddToColl, CollectionID: col.ID},
		{ItemIDs: []string{a.ID}, Action: BulkClearSale},
	} {
		if _, err := m.Bulk(ctx, "i1", in); err != nil {
			t.Fatalf("%s: %v", in.Action, err)
		}
	}
	it, _ := m.Item(ctx, "i1", b.ID)
	if it.Available || !it.Hidden || it.CategoryID != drinks.ID || len(it.CollectionIDs) != 1 {
		t.Fatalf("after bulk = %+v", it)
	}
	if it, _ := m.Item(ctx, "i1", a.ID); it.SalePriceCents != 0 {
		t.Fatal("clear_sale")
	}
	for _, in := range []MenuBulkInput{
		{ItemIDs: []string{a.ID}, Action: "explode"},
		{ItemIDs: []string{a.ID}, Action: BulkSetAvailable},
		{ItemIDs: []string{a.ID}, Action: BulkAdjustPrice},
		{ItemIDs: []string{a.ID}, Action: BulkMoveCategory, CategoryID: sptr("mcat_0000000000000000")},
		{ItemIDs: []string{"x"}, Action: BulkSetHidden, Hidden: bptr(true)},
	} {
		if _, err := m.Bulk(ctx, "i1", in); err == nil {
			t.Fatalf("%+v accepted", in)
		}
	}
	// CSV export: every item, reimportable.
	out, err := m.ExportCSV(ctx, "i1")
	if err != nil || !strings.Contains(string(out), "Pizza Calabresa (cópia)") || !strings.Contains(string(out), ";Promo") {
		t.Fatalf("export = %s %v", out, err)
	}
	if pv, err := m.ImportCSV(ctx, "i1", MenuCSVInput{CSV: string(out), MenuImportOptions: MenuImportOptions{DryRun: true}}); err != nil || pv.Valid != 5 || pv.Invalid != 0 {
		t.Fatalf("reimport preview = %+v %v", pv, err)
	}
	// Bulk delete removes items and photos.
	if _, err := m.Bulk(ctx, "i1", MenuBulkInput{ItemIDs: []string{a.ID, cp.ID, c.ID}, Action: BulkDelete}); err != nil {
		t.Fatal(err)
	}
	if v, _ := m.Get(ctx, "i1"); v.ItemCount != 2 {
		t.Fatalf("after delete = %d", v.ItemCount)
	}
}

func TestMenuImportRowsNewFields(t *testing.T) {
	e := newMsgEnv(t)
	m := newMenu(e)
	ctx := context.Background()
	v, err := m.ImportRows(ctx, "i1", MenuRowsInput{Rows: []menu.Row{
		{Category: "Pizzas", Name: "Calabresa", Price: "50", SalePrice: "39,90", SaleUntil: "2099-12-31", LinkURL: "https://x.example/c",
			SKU: "CAL", Hidden: true, Collections: []string{"Promoções", "Mais vendidos"}, Available: true},
		{Name: "Coca", Price: "12", SalePrice: "15", Available: true},
	}})
	if err == nil || v.Invalid != 1 || !strings.Contains(strings.Join(v.Errors, ";"), "linha 2: preço promocional (R$ 15,00) deve ser menor") {
		t.Fatalf("view = %+v %v", v, err)
	}
	v, err = m.ImportRows(ctx, "i1", MenuRowsInput{Rows: v.Rows[:1]})
	if err != nil || v.ItemsCreated != 1 || v.CollectionsCreated != 2 {
		t.Fatalf("saved = %+v %v", v, err)
	}
	all, _ := m.Search(ctx, "i1", MenuSearchInput{})
	it := all.Items[0]
	if it.SalePriceCents != 3990 || !it.Hidden || it.LinkURL != "https://x.example/c" || it.SKU != "CAL" || len(it.CollectionIDs) != 2 {
		t.Fatalf("item = %+v", it)
	}
}

func TestMenuOrderDrafts(t *testing.T) {
	e := newMsgEnv(t)
	m := newMenu(e)
	ctx := context.Background()
	opts := []menu.Option{{Name: "Tamanho", Required: true, Choices: []menu.Choice{{Name: "P"}, {Name: "G", PriceDeltaCents: 1000}}},
		{Name: "Borda", Choices: []menu.Choice{{Name: "Catupiry", PriceDeltaCents: 800}}}}
	pz, _ := m.CreateItem(ctx, "i1", MenuItemInput{Name: sptr("Calabresa"), PriceCents: i64(5000), SalePriceCents: i64(3990), Options: &opts})
	coca, _ := m.CreateItem(ctx, "i1", MenuItemInput{Name: sptr("Coca"), PriceCents: i64(1200)})
	gone, _ := m.CreateItem(ctx, "i1", MenuItemInput{Name: sptr("Marguerita"), PriceCents: i64(4200), Available: bptr(false)})
	hid, _ := m.CreateItem(ctx, "i1", MenuItemInput{Name: sptr("Secreta"), PriceCents: i64(100), Hidden: bptr(true)})
	in := OrderDraftInput{Chat: "5511988887777", CustomerName: "Ana", Items: []menu.OrderLineInput{
		{ItemID: pz.ID, Quantity: 2, Options: []string{"G", "Catupiry"}}, {ItemID: coca.ID, Quantity: 1}}}
	// dry_run prices without saving: (39,90 + 10 + 8) x 2 + 12 = 127,80.
	in.DryRun = true
	pv, err := m.CreateOrderDraft(ctx, "i1", in)
	if err != nil || pv.ID != "" || pv.TotalCents != 12780 || pv.SavingsCents != 2020 || !pv.Valid || !strings.Contains(pv.Summary, "*Total: R$ 127,80*") {
		t.Fatalf("preview = %+v %v", pv, err)
	}
	in.DryRun = false
	d, err := m.CreateOrderDraft(ctx, "i1", in)
	if err != nil || d.ID == "" || d.Status != "draft" || d.Chat != "5511988887777@s.whatsapp.net" || d.TotalCents != 12780 || d.Source != "api" {
		t.Fatalf("draft = %+v %v", d, err)
	}
	// Sold out, hidden and missing required options refuse the save.
	for _, li := range []menu.OrderLineInput{{ItemID: gone.ID, Quantity: 1}, {ItemID: hid.ID, Quantity: 1}, {ItemID: pz.ID, Quantity: 1},
		{ItemID: pz.ID, Quantity: 1, Options: []string{"Gigante"}}, {Name: "Coxinha", Quantity: 1}} {
		v, err := m.CreateOrderDraft(ctx, "i1", OrderDraftInput{Items: []menu.OrderLineInput{li}})
		if apiCode(err) != "validation_failed" || v.Valid || len(v.Problems) == 0 {
			t.Fatalf("%+v = %+v %v", li, v, err)
		}
	}
	if _, err := m.CreateOrderDraft(ctx, "i1", OrderDraftInput{}); apiCode(err) != "validation_failed" {
		t.Fatalf("empty order = %v", err)
	}
	// Send the summary: through the queue, the draft becomes sent.
	res, err := m.SendOrderSummary(ctx, "i1", d.ID, SendOrderInput{Intro: "Confere?"}, 2*time.Second)
	if err != nil || res.Draft.Status != "sent" || res.Draft.MessageID == "" {
		t.Fatalf("send = %+v %v", res, err)
	}
	if r := e.lastSent(); !strings.HasPrefix(r.Text, "Confere?\n\n*Resumo do pedido*") || !strings.Contains(r.Text, "2x Calabresa — R$ 115,80") {
		t.Fatalf("summary = %q", r.Text)
	}
	again, err := m.SendOrderSummary(ctx, "i1", d.ID, SendOrderInput{Intro: "Confere?"}, 0)
	if err != nil || again.Message.ID != res.Message.ID {
		t.Fatalf("a retry sent twice: %+v %v", again, err)
	}
	if v, err := m.SetOrderStatus(ctx, "i1", d.ID, OrderStatusInput{Status: "confirmed"}); err != nil || v.Status != "confirmed" {
		t.Fatal(v, err)
	}
	if _, err := m.SetOrderStatus(ctx, "i1", d.ID, OrderStatusInput{Status: "draft"}); apiCode(err) != "conflict" {
		t.Fatalf("confirmed back to draft = %v", err)
	}
	if _, err := m.SetOrderStatus(ctx, "i1", d.ID, OrderStatusInput{Status: "paid"}); apiCode(err) != "validation_failed" {
		t.Fatalf("unknown status = %v", err)
	}
	m.SetOrderStatus(ctx, "i1", d.ID, OrderStatusInput{Status: "cancelled"})
	if _, err := m.SendOrderSummary(ctx, "i1", d.ID, SendOrderInput{}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("cancelled draft sent: %v", err)
	}
	list, err := m.ListOrderDrafts(ctx, "i1", OrderDraftQuery{Chat: "5511988887777"})
	if err != nil || len(list.Drafts) != 1 || list.Drafts[0].Status != "cancelled" {
		t.Fatalf("list = %+v %v", list, err)
	}
	if _, err := m.OrderDraft(ctx, "i1", "odr_0000000000000000"); apiCode(err) != "not_found" {
		t.Fatalf("unknown draft = %v", err)
	}
}

// The WhatsApp catalog copy brings every photo, the link, retailer_id as the sku, the
// sale price and the collections (read only: nothing is written to WhatsApp).
func TestMenuImportCatalogFull(t *testing.T) {
	e := newSocialEnv(t)
	f := withCatalog(t, e)
	f.Catalogs[ownJID] = []catalogtest.Product{
		{ID: "101", Name: "Camiseta", Price: 59900, SalePrice: 49900, Currency: "BRL", Photos: 3, RetailerID: "CAM-01", URL: "https://loja.example/cam"},
		{ID: "102", Name: "Caneca", Price: 25000, Currency: "BRL", NoImage: true},
	}
	f.Collections[ownJID] = map[string][]string{"Verão": {"101", "102"}, "Presentes": {"102"}}
	m := newMenu(e.msgEnv)
	ctx := context.Background()
	res, err := m.ImportCatalog(ctx, "i1", MenuCatalogInput{})
	if err != nil || res.ItemsCreated != 2 || res.ImagesSaved != 3 || res.CollectionsCreated != 2 {
		t.Fatalf("copy = %+v %v", res, err)
	}
	all, _ := m.Search(ctx, "i1", MenuSearchInput{})
	cam := all.Items[0]
	if all.Items[1].Name == "Camiseta" {
		cam = all.Items[1]
	}
	if cam.SKU != "CAM-01" || cam.LinkURL != "https://loja.example/cam" || cam.PriceCents != 5990 || cam.SalePriceCents != 4990 ||
		len(cam.Images) != 3 || len(cam.CollectionIDs) != 1 {
		t.Fatalf("camiseta = %+v", cam)
	}
	colls, _ := m.ListCollections(ctx, "i1")
	byName := map[string]MenuCollectionView{}
	for _, c := range colls {
		byName[c.Name] = c
	}
	if byName["Verão"].ItemCount != 2 || byName["Presentes"].ItemCount != 1 {
		t.Fatalf("collections = %+v", colls)
	}
	// A second copy skips everything and creates no new collection.
	res, err = m.ImportCatalog(ctx, "i1", MenuCatalogInput{})
	if err != nil || res.ItemsCreated != 0 || res.Skipped != 2 || res.CollectionsCreated != 0 {
		t.Fatalf("second copy = %+v %v", res, err)
	}
	if n, _ := e.st.MenuCollectionCount(ctx, "i1"); n != 2 {
		t.Fatalf("collections = %d", n)
	}
}

// Concurrent collection writes through the service stay within the limits (-race).
func TestMenuCollectionsConcurrent(t *testing.T) {
	e := newMsgEnv(t)
	m := newMenu(e)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < store.MaxMenuCollections+5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = m.CreateCollection(ctx, "i1", MenuCollectionInput{Name: sptr("c" + string(rune('a'+i%26)) + string(rune('a'+i/26)))})
		}(i)
	}
	wg.Wait()
	if list, _ := m.ListCollections(ctx, "i1"); len(list) != store.MaxMenuCollections {
		t.Fatalf("collections = %d", len(list))
	}
}
