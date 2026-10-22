package attendant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/store"
)

// Hidden items never reach the attendant (not in the prompt, not priced); a sale price
// is the price it charges; the order is stored as the chat's order draft, the same
// drafts create_order_draft makes, and updated when the customer changes it.
func TestMenuHiddenSaleAndDraft(t *testing.T) {
	e := newTEnv(t)
	e.configure(SettingsPatch{})
	ctx := context.Background()
	cat, _ := e.st.CreateMenuCategory(ctx, inst, "Pizzas")
	if _, err := e.st.CreateMenuItem(ctx, inst, store.MenuItem{CategoryID: cat.ID, Name: "Calabresa", PriceCents: 5000, SalePriceCents: 3990,
		Available: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateMenuItem(ctx, inst, store.MenuItem{CategoryID: cat.ID, Name: "Pizza Secreta", PriceCents: 100, Available: true,
		Hidden: true}); err != nil {
		t.Fatal(err)
	}
	col, _ := e.st.CreateMenuCollection(ctx, inst, store.MenuCollection{Name: "Promoções", Active: true})
	off, _ := e.st.CreateMenuCollection(ctx, inst, store.MenuCollection{Name: "Rascunho", Active: false})
	items, _ := e.st.ListMenuItems(ctx, inst)
	_ = e.st.AddMenuCollectionItems(ctx, inst, col.ID, []string{items[0].ID})
	_ = e.st.AddMenuCollectionItems(ctx, inst, off.ID, []string{items[0].ID})
	v := e.agent(Profile{Name: "Ana", Mode: "pedido"})
	if _, err := e.ad.ActivateAgent(ctx, inst, v.ID); err != nil {
		t.Fatal(err)
	}
	order := func(qty int, name string) {
		e.chat.answer = jsonAnswer(map[string]any{"reply": "Resumo. Confirma?", "handoff": false, "handoff_note": "",
			"event": map[string]any{"type": "order_draft", "customer_name": "Ana", "address": "Rua B, 5", "notes": "",
				"items": []any{map[string]any{"name": name, "quantity": qty, "unit_price": 50, "options": []any{}}}}})
	}
	order(2, "Calabresa")
	resp, err := e.att.Answer(ctx, request(ctxMsg("m1", "contact", "2 calabresas", time.Now())))
	if err != nil {
		t.Fatal(err)
	}
	s := e.chat.last().System
	if strings.Contains(s, "Secreta") {
		t.Fatalf("a hidden item reached the prompt:\n%s", s)
	}
	if !strings.Contains(s, "- Calabresa — BRL 39.90 (promoção, de 50.00) · categoria: Pizzas · coleções: Promoções\n") || strings.Contains(s, "Rascunho") {
		t.Fatalf("sale / collections line:\n%s", s)
	}
	if len(resp.Events) != 1 || resp.Events[0].Type != events.TypeAgentOrderDraft {
		t.Fatalf("events = %+v", resp.Events)
	}
	d := resp.Events[0].Data
	first := d["items"].([]map[string]any)[0]
	if first["unit_price"] != 39.9 || d["total"] != 79.8 || first["on_sale"] != true {
		t.Fatalf("priced = %+v total %v", first, d["total"])
	}
	id, _ := d["order_draft_id"].(string)
	if id == "" {
		t.Fatalf("no order_draft_id in %+v", d)
	}
	dr, err := e.st.GetOrderDraft(ctx, inst, id)
	if err != nil || dr.Chat != chatJID || dr.Source != "attendant" || dr.TotalCents != 7980 || !dr.Valid || dr.CustomerName != "Ana" || dr.Address != "Rua B, 5" {
		t.Fatalf("draft = %+v %v", dr, err)
	}
	// The customer changes the order: the same draft is updated.
	order(3, "Calabresa")
	resp, _ = e.att.Answer(ctx, request(ctxMsg("m2", "contact", "na verdade 3", time.Now())))
	if got := resp.Events[0].Data["order_draft_id"]; got != id {
		t.Fatalf("a new draft (%v) instead of updating %s", got, id)
	}
	if dr, _ := e.st.GetOrderDraft(ctx, inst, id); dr.TotalCents != 11970 {
		t.Fatalf("updated total = %d", dr.TotalCents)
	}
	// A hidden item is not priced from the menu, even by its exact name.
	order(1, "Pizza Secreta")
	resp, _ = e.att.Answer(ctx, request(ctxMsg("m3", "contact", "a secreta", time.Now())))
	if it := resp.Events[0].Data["items"].([]map[string]any)[0]; it["in_catalog"] != false {
		t.Fatalf("hidden item priced from the menu: %+v", it)
	}
	list, _ := e.st.ListOrderDrafts(ctx, inst, store.OrderDraftFilter{})
	if len(list) != 1 || list[0].Valid {
		t.Fatalf("drafts = %+v", list)
	}
}
