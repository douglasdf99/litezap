package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/menu"
	"github.com/douglasdf99/litezap/internal/service"
)

func TestMenuTools(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("M")
			ctx := context.Background()
			cs := e.client(m, tok)
			var empty menuOutput
			ok(t, cs, "get_menu", nil, &empty)
			if empty.ItemCount != 0 || !strings.Contains(empty.Note, "empty") {
				t.Fatalf("empty menu = %+v", empty)
			}
			cat, err := e.menu.CreateCategory(ctx, id, service.MenuCategoryInput{Name: "Pizzas"})
			if err != nil {
				t.Fatal(err)
			}
			name, price := "Calabresa", int64(4590)
			opts := []menu.Option{{Name: "Tamanho", Required: true, Choices: []menu.Choice{{Name: "P"}, {Name: "G", PriceDeltaCents: 1000}}}}
			tags := []string{"picante", "mais pedida"}
			it, err := e.menu.CreateItem(ctx, id, service.MenuItemInput{CategoryID: &cat.ID, Name: &name, PriceCents: &price, Options: &opts, Tags: &tags})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.menu.CreateCategory(ctx, id, service.MenuCategoryInput{Name: "Sobremesas"}); err != nil {
				t.Fatal(err)
			}
			sold := "Marguerita"
			off := false
			gone, err := e.menu.CreateItem(ctx, id, service.MenuItemInput{CategoryID: &cat.ID, Name: &sold, PriceCents: &price, Available: &off})
			if err != nil {
				t.Fatal(err)
			}
			var v menuOutput
			ok(t, cs, "get_menu", nil, &v)
			if v.ItemCount != 2 || v.Categories[0].Items[0].Price != "R$ 45,90" || len(v.Categories[0].Items[0].Options) != 1 ||
				strings.Join(v.Categories[0].Items[0].Tags, ",") != "picante,mais pedida" || len(v.Categories) != 1 {
				t.Fatalf("menu = %+v", v)
			}
			// include_empty lists the empty categories too (management: find and delete them).
			ok(t, cs, "get_menu", map[string]any{"include_empty": true}, &v)
			if len(v.Categories) != 2 || v.Categories[1].Name != "Sobremesas" || v.Categories[1].ItemCount != 0 || v.ItemCount != 2 {
				t.Fatalf("include_empty = %+v", v)
			}
			ok(t, cs, "get_menu", map[string]any{"include_unavailable": false}, &v)
			if v.ItemCount != 1 {
				t.Fatalf("available only = %+v", v)
			}
			var one menuItemOut
			ok(t, cs, "get_menu_item", map[string]any{"item_id": it.ID}, &one)
			if one.Name != "Calabresa" || one.HasPhoto || len(one.Tags) != 2 {
				t.Fatalf("item = %+v", one)
			}
			fails(t, cs, "get_menu_item", map[string]any{"item_id": "mitm_0000000000000000"}, "not_found")

			var out sendOutput
			ok(t, cs, "send_menu_item", map[string]any{"to": "5511977776666", "item_id": it.ID, "caption": "Olha só"}, &out)
			if !out.Sent || out.Message.Type != "text" {
				t.Fatalf("send = %+v", out)
			}
			sent := e.sp.Last(id).WA.Sent()
			if txt := sent[len(sent)-1].Text; !strings.HasPrefix(txt, "Olha só\n\n*Calabresa* — R$ 45,90") || !strings.Contains(txt, "G (+R$ 10,00)") {
				t.Fatalf("text = %q", txt)
			}
			fails(t, cs, "send_menu_item", map[string]any{"to": "5511977776666", "item_id": gone.ID}, "esgotado")

			var mo sendMenuOutput
			ok(t, cs, "send_menu", map[string]any{"to": "5511977776666"}, &mo)
			if len(mo.Messages) != 1 || mo.Message.ID == "" {
				t.Fatalf("send_menu = %+v", mo)
			}
			eventually(t, "menu sent", func() bool {
				s := e.sp.Last(id).WA.Sent()
				return strings.Contains(s[len(s)-1].Text, "*Pizzas*") && !strings.Contains(s[len(s)-1].Text, "Marguerita")
			})
		})
	}
}
