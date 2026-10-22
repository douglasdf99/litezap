package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/menu"
	"github.com/douglasdf99/litezap/internal/service"
)

func pngB64(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	img.Set(1, 1, color.RGBA{0, 200, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func sp(s string) *string { return &s }
func ip(n int64) *int64   { return &n }

// seedCatalog builds a small menu: a pizza on sale with options and two photos, a drink,
// a hidden item and a sold-out one, and a "Promoções" collection.
func seedCatalog(t *testing.T, e *env, id string) (pizza, coca, hidden, gone service.MenuItemView, col service.MenuCollectionDetail) {
	t.Helper()
	ctx := context.Background()
	cat, err := e.menu.CreateCategory(ctx, id, service.MenuCategoryInput{Name: "Pizzas"})
	if err != nil {
		t.Fatal(err)
	}
	opts := []menu.Option{{Name: "Tamanho", Required: true, Choices: []menu.Choice{{Name: "P"}, {Name: "G", PriceDeltaCents: 1000}}}}
	pizza, err = e.menu.CreateItem(ctx, id, service.MenuItemInput{CategoryID: &cat.ID, Name: sp("Calabresa"), PriceCents: ip(5000),
		SalePriceCents: ip(3990), Options: &opts, Tags: &[]string{"picante"}})
	if err != nil {
		t.Fatal(err)
	}
	e.menu.SetImage(ctx, id, pizza.ID, service.MenuImageInput{Base64: pngB64(t)})
	pizza, _ = e.menu.AddImage(ctx, id, pizza.ID, service.MenuImageInput{Base64: pngB64(t)})
	coca, _ = e.menu.CreateItem(ctx, id, service.MenuItemInput{Name: sp("Coca"), PriceCents: ip(1200)})
	yes, no := true, false
	hidden, _ = e.menu.CreateItem(ctx, id, service.MenuItemInput{CategoryID: &cat.ID, Name: sp("Pizza Secreta"), PriceCents: ip(100), Hidden: &yes})
	gone, _ = e.menu.CreateItem(ctx, id, service.MenuItemInput{CategoryID: &cat.ID, Name: sp("Marguerita"), PriceCents: ip(4200), Available: &no})
	col, err = e.menu.CreateCollection(ctx, id, service.MenuCollectionInput{Name: sp("Promoções"), ItemIDs: &[]string{pizza.ID, hidden.ID, coca.ID}})
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestMenuCatalogReadAndSendTools(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("CAT")
			cs := e.client(m, tok)
			pizza, _, hidden, _, col := seedCatalog(t, e, id)
			var sr searchMenuOutput
			ok(t, cs, "search_menu", map[string]any{"query": "pizza"}, &sr)
			if sr.Total != 1 || !sr.Items[0].Hidden { // the assistant sees hidden items (flagged)
				t.Fatalf("search = %+v", sr)
			}
			ok(t, cs, "search_menu", map[string]any{"collection": "promoções", "on_sale": true}, &sr)
			if sr.Total != 1 || sr.Items[0].ID != pizza.ID || sr.Items[0].PriceLabel != "~R$ 50,00~ *R$ 39,90*" || sr.Items[0].EffectivePriceCents != 3990 ||
				sr.Items[0].PhotoCount != 2 || len(sr.Items[0].Tags) != 1 {
				t.Fatalf("search by collection = %+v", sr)
			}
			ok(t, cs, "search_menu", map[string]any{"category": "Pizzas", "available": false}, &sr)
			if sr.Total != 1 || sr.Items[0].Name != "Marguerita" {
				t.Fatalf("search by category = %+v", sr)
			}
			ok(t, cs, "search_menu", map[string]any{"hidden": true}, &sr)
			if sr.Total != 1 || !sr.Items[0].Hidden || sr.Items[0].ID != hidden.ID {
				t.Fatalf("search hidden = %+v", sr)
			}
			fails(t, cs, "search_menu", map[string]any{"collection": "Natal"}, "collection not found")
			var gc getCollectionOutput
			ok(t, cs, "get_collection", nil, &gc)
			if len(gc.Collections) != 1 || gc.Collections[0].Name != "Promoções" || gc.Collections[0].ItemCount != 3 {
				t.Fatalf("list = %+v", gc)
			}
			var byName getCollectionOutput
			ok(t, cs, "get_collection", map[string]any{"name": "promocoes"}, &byName) // accents and case ignored
			if byName.Collection == nil || byName.Collection.ID != col.ID {
				t.Fatalf("by name = %+v", byName)
			}
			ok(t, cs, "get_collection", map[string]any{"collection_id": col.ID}, &gc)
			if gc.Collection == nil || len(gc.Collection.Items) != 3 || gc.Collection.Items[0].ID != pizza.ID {
				t.Fatalf("collection = %+v", gc)
			}
			// The menu shows tags, collections, sale and hidden flags.
			var mv menuOutput
			ok(t, cs, "get_menu", nil, &mv)
			if len(mv.Collections) != 1 || mv.ItemCount != 4 {
				t.Fatalf("menu = %+v", mv)
			}
			var one menuItemOut
			ok(t, cs, "get_menu_item", map[string]any{"item_id": pizza.ID}, &one)
			if len(one.Photos) != 2 || !one.Photos[0].Main || one.SalePrice != "R$ 39,90" || !one.OnSale {
				t.Fatalf("item = %+v", one)
			}
			// send_collection: the text and photos, through the queue.
			var so sendMenuOutput
			ok(t, cs, "send_collection", map[string]any{"to": "5511977776666", "name": "Promoções", "photos": 2}, &so)
			if len(so.Messages) != 2 {
				t.Fatalf("send_collection = %+v", so)
			}
			eventually(t, "collection sent", func() bool {
				for _, r := range e.sp.Last(id).WA.Sent() {
					if strings.HasPrefix(r.Text, "*Promoções*") && strings.Contains(r.Text, "~R$ 50,00~ *R$ 39,90*") && !strings.Contains(r.Text, "Secreta") {
						return true
					}
				}
				return false
			})
			// Album: both photos of the pizza.
			var al sendOutput
			ok(t, cs, "send_menu_item", map[string]any{"to": "5511977776666", "item_id": pizza.ID, "album": true}, &al)
			if len(al.Messages) != 2 {
				t.Fatalf("album = %+v", al)
			}
			// send_order_summary with items: priced by the menu, never by the caller.
			var os sendOrderSummaryOutput
			ok(t, cs, "send_order_summary", map[string]any{"to": "5511977776666", "intro": "Confere:",
				"items": []any{map[string]any{"item_id": pizza.ID, "quantity": 2, "options": []any{"G"}}}}, &os)
			if os.Draft.TotalCents != 9980 || os.Draft.Status != "sent" || os.Draft.Source != "mcp" {
				t.Fatalf("summary = %+v", os)
			}
			fails(t, cs, "send_order_summary", map[string]any{"to": "5511977776666", "items": []any{map[string]any{"item_id": pizza.ID, "quantity": 1}}},
				"escolha Tamanho")
			fails(t, cs, "send_order_summary", map[string]any{"to": "5511900000001", "order_id": os.Draft.ID}, "another chat")
			fails(t, cs, "send_order_summary", map[string]any{"to": "5511977776666", "items": []any{map[string]any{"item_id": hidden.ID, "quantity": 1}}},
				"não encontrado")
		})
	}
}

func TestMenuCatalogAssistantTools(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("ED")
			cs := e.client(m, tok)
			pizza, coca, _, gone, col := seedCatalog(t, e, id)
			// Collections.
			var gc getCollectionOutput
			ok(t, cs, "create_menu_collection", map[string]any{"name": "Mais vendidos", "item_ids": []any{coca.ID}, "cover_item_id": pizza.ID}, &gc)
			if gc.Collection == nil || gc.Collection.ItemCount != 1 || !gc.Collection.HasCover {
				t.Fatalf("created = %+v", gc)
			}
			best := gc.Collection.ID
			ok(t, cs, "set_menu_collection_items", map[string]any{"collection_id": best, "action": "add", "item_ids": []any{pizza.ID}}, &gc)
			if gc.Collection.ItemCount != 2 {
				t.Fatalf("add = %+v", gc)
			}
			var ro menuReordered
			ok(t, cs, "reorder_menu_collections", map[string]any{"collection_id": best, "items": []any{pizza.ID, coca.ID}}, &ro)
			ok(t, cs, "reorder_menu_collections", map[string]any{"collections": []any{best, col.ID}}, &ro)
			ok(t, cs, "update_menu_collection", map[string]any{"collection_id": best, "active": false, "description": "Os queridinhos"}, &gc)
			if gc.Collection.Active || gc.Collection.Description != "Os queridinhos" {
				t.Fatalf("update = %+v", gc)
			}
			ok(t, cs, "set_menu_collection_items", map[string]any{"collection_id": best, "action": "remove", "item_ids": []any{coca.ID}}, &gc)
			// Items: sale, link, sku, hidden and collections by name.
			var it menuItemFull
			ok(t, cs, "create_menu_item", map[string]any{"name": "Suco", "price": "10", "sale_price": "8,50", "sale_until": "31/12/2099",
				"sku": "SUC-1", "link_url": "https://loja.example/suco", "collections": []any{"Promoções", "Bebidas geladas"}, "create_collections": true}, &it)
			if it.SalePrice != "R$ 8,50" || it.SKU != "SUC-1" || it.LinkURL != "https://loja.example/suco" || len(it.CollectionIDs) != 2 {
				t.Fatalf("created item = %+v", it)
			}
			fails(t, cs, "create_menu_item", map[string]any{"name": "X", "price": "10", "sale_price": "11"}, "menor que o preço")
			fails(t, cs, "create_menu_item", map[string]any{"name": "X", "collections": []any{"Natal"}}, "create_collections=true")
			var up menuItemFull
			ok(t, cs, "update_menu_item", map[string]any{"item_id": it.ID, "hidden": true, "sale_price": "", "collections": []any{}}, &up)
			if !up.Hidden || up.SalePrice != "" || up.SaleUntil != "" || len(up.CollectionIDs) != 0 {
				t.Fatalf("updated item = %+v", up)
			}
			// Photos.
			var pv menuItemFull
			ok(t, cs, "get_menu_item", map[string]any{"item_id": pizza.ID}, &pv)
			second := pv.Photos[1].ID
			ok(t, cs, "set_menu_item_main_photo", map[string]any{"item_id": pizza.ID, "photo_id": second}, &pv)
			if pv.Photos[0].ID != second {
				t.Fatalf("main = %+v", pv.Photos)
			}
			ok(t, cs, "reorder_menu_item_photos", map[string]any{"item_id": pizza.ID, "photo_ids": []any{pv.Photos[1].ID, second}}, &pv)
			if pv.Photos[1].ID != second {
				t.Fatalf("reorder = %+v", pv.Photos)
			}
			ok(t, cs, "remove_menu_item_photo", map[string]any{"item_id": pizza.ID, "photo_id": second}, &pv)
			if pv.PhotoCount != 1 {
				t.Fatalf("remove = %+v", pv)
			}
			fails(t, cs, "add_menu_item_photo", map[string]any{"item_id": pizza.ID, "url": "nao-e-um-endereco"}, "url")
			// Duplicate.
			var dup menuItemFull
			ok(t, cs, "duplicate_menu_item", map[string]any{"item_id": pizza.ID, "name": "Calabresa Grande"}, &dup)
			if dup.Name != "Calabresa Grande" || dup.ID == pizza.ID || dup.PhotoCount != 1 || len(dup.Options) != 1 {
				t.Fatalf("duplicate = %+v", dup)
			}
			// Bulk: the preview is the default and saves nothing.
			var bo bulkOutput
			ok(t, cs, "bulk_update_menu_items", map[string]any{"item_ids": []any{pizza.ID, dup.ID}, "action": "adjust_price", "amount": "+2,00"}, &bo)
			if !bo.DryRun || bo.Affected != 2 || !strings.Contains(bo.Note, "PREVIEW ONLY") || bo.Changes[0].NewPriceCents != 5200 {
				t.Fatalf("preview = %+v", bo)
			}
			ok(t, cs, "get_menu_item", map[string]any{"item_id": pizza.ID}, &pv)
			if pv.PriceCents != 5000 {
				t.Fatal("the preview saved")
			}
			ok(t, cs, "bulk_update_menu_items", map[string]any{"item_ids": []any{pizza.ID, dup.ID}, "action": "adjust_price", "percent": 10, "dry_run": false}, &bo)
			ok(t, cs, "get_menu_item", map[string]any{"item_id": pizza.ID}, &pv)
			if pv.PriceCents != 5500 || pv.EffectivePriceCents != 4389 {
				t.Fatalf("after +10%% = %+v", pv)
			}
			ok(t, cs, "bulk_update_menu_items", map[string]any{"item_ids": []any{dup.ID}, "action": "delete"}, &bo)
			if !strings.Contains(bo.Note, "explicit confirmation") {
				t.Fatalf("delete preview note = %q", bo.Note)
			}
			ok(t, cs, "bulk_update_menu_items", map[string]any{"item_ids": []any{dup.ID, gone.ID}, "action": "move_category", "category": "Outros", "dry_run": false}, &bo)
			// Orders.
			var od orderDraftOut
			ok(t, cs, "create_order_draft", map[string]any{"chat": "5511977776666", "customer_name": "Ana",
				"items": []any{map[string]any{"item_id": pizza.ID, "quantity": 1, "options": []any{"Tamanho: P"}}, map[string]any{"item_id": coca.ID, "quantity": 2}}}, &od)
			if od.ID == "" || od.TotalCents != 4389+2400 || od.Status != "draft" || !strings.Contains(od.Summary, "Cliente: Ana") {
				t.Fatalf("order = %+v", od)
			}
			fails(t, cs, "create_order_draft", map[string]any{"items": []any{map[string]any{"item_id": gone.ID, "quantity": 1}}}, "esgotado")
			var lo orderDraftsOut
			ok(t, cs, "list_order_drafts", map[string]any{"status": "draft"}, &lo)
			if len(lo.Drafts) != 1 || lo.Drafts[0].ID != od.ID {
				t.Fatalf("list = %+v", lo)
			}
			ok(t, cs, "update_order_draft_status", map[string]any{"order_id": od.ID, "status": "confirmed"}, &od)
			if od.Status != "confirmed" {
				t.Fatalf("status = %+v", od)
			}
			fails(t, cs, "update_order_draft_status", map[string]any{"order_id": od.ID, "status": "draft"}, "not allowed")
			ok(t, cs, "get_order_draft", map[string]any{"order_id": od.ID}, &od)
			// Text and CSV.
			var mt menuTextOutput
			ok(t, cs, "get_menu_text", map[string]any{"collection": "Promoções"}, &mt)
			if len(mt.Chunks) != 1 || !strings.HasPrefix(mt.Chunks[0], "*Promoções*") || strings.Contains(mt.Chunks[0], "Secreta") {
				t.Fatalf("text = %+v", mt)
			}
			var ex exportCSVOutput
			ok(t, cs, "export_menu_csv", nil, &ex)
			if !strings.Contains(ex.CSV, "categoria;nome;descricao;preco;preco_promocional") || !strings.Contains(ex.CSV, "Calabresa") || ex.Items < 4 {
				t.Fatalf("csv = %+v", ex)
			}
			var del menuDeleted
			ok(t, cs, "delete_menu_collection", map[string]any{"collection_id": best}, &del)
		})
	}
}

// The agents' table never shows hidden items or inactive collections, whatever they ask.
func TestMenuToolsHideHiddenFromAgents(t *testing.T) {
	e := newEnv(t, nil)
	id, tok := e.connected("AG")
	ctx := context.Background()
	pizza, _, hidden, _, col := seedCatalog(t, e, id)
	inactive, _ := e.menu.CreateCollection(ctx, id, service.MenuCollectionInput{Name: sp("Rascunho"), Active: func() *bool { b := false; return &b }(),
		ItemIDs: &[]string{pizza.ID}})
	scope, err := e.svc.Authenticate(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	b := &Local{Instances: e.svc, Messaging: e.msg, Events: e.bus, Caller: service.Scope{InstanceID: scope}, Menu: e.menu}
	defs := map[string]ToolDef{}
	for _, d := range AgentCatalog(b, nil) {
		defs[d.Name] = d
	}
	run := func(name string, args map[string]any) (string, error) {
		raw, _ := json.Marshal(args)
		out, err := defs[name].Call(ctx, raw)
		return string(out), err
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"get_menu", nil},
		{"search_menu", map[string]any{"query": "pizza", "hidden": true}},
		{"get_collection", map[string]any{"collection_id": col.ID}},
	} {
		out, err := run(c.tool, c.args)
		if err != nil {
			t.Fatalf("%s: %v", c.tool, err)
		}
		if strings.Contains(out, "Secreta") || strings.Contains(out, hidden.ID) || strings.Contains(out, "Rascunho") {
			t.Fatalf("%s showed a hidden item or inactive collection: %s", c.tool, out)
		}
	}
	if _, err := run("get_menu_item", map[string]any{"item_id": hidden.ID}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("get_menu_item of a hidden item = %v", err)
	}
	// Counts and ids do not give hidden items or inactive collections away either.
	out, _ := run("get_menu", nil)
	var mv menuOutput
	_ = json.Unmarshal([]byte(out), &mv)
	if len(mv.Collections) != 1 || mv.Collections[0].ItemCount != 2 || mv.Categories[0].ItemCount != 2 || strings.Contains(out, inactive.ID) {
		t.Fatalf("agent menu counts = %s", out)
	}
	out, _ = run("get_collection", nil)
	if !strings.Contains(out, `"item_count":2`) || strings.Contains(out, "Rascunho") {
		t.Fatalf("agent collection list = %s", out)
	}
	// An agent never sends a draft that has no chat (it would go to the trigger chat).
	d, err := e.menu.CreateOrderDraft(ctx, id, service.OrderDraftInput{Items: []menu.OrderLineInput{{ItemID: col.Items[2].ID, Quantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run("send_order_summary", map[string]any{"to": "5511977776666", "order_id": d.ID}); err == nil || !strings.Contains(err.Error(), "no chat") {
		t.Fatalf("agent sent a chatless draft: %v", err)
	}
	if _, err := run("get_collection", map[string]any{"collection_id": inactive.ID}); err == nil {
		t.Fatal("an agent read an inactive collection")
	}
	// The assistant's table shows them (flagged).
	all := map[string]ToolDef{}
	for _, d := range Catalog(b, nil) {
		all[d.Name] = d
	}
	raw, _ := json.Marshal(map[string]any{"hidden": true})
	sr, err := all["search_menu"].Call(ctx, raw)
	if err != nil || !strings.Contains(string(sr), "Secreta") {
		t.Fatalf("assistant search hidden = %s %v", sr, err)
	}
}
