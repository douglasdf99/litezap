package mcp

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/menu"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
)

// import_menu_rows: the preview saves nothing and lists the problems, a save with errors
// is refused, a clean save creates categories and items, and a second preview flags the
// rows already in the menu.
func TestImportMenuRowsTool(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			_, tok := e.connected("IMP")
			cs := e.client(m, tok)
			rows := []map[string]any{
				{"category": "Pizzas", "name": "Calabresa", "price": "45,90",
					"options": []map[string]any{{"name": "Tamanho", "required": true, "choices": []map[string]any{{"name": "P"}, {"name": "G", "price_delta": "+10,00"}}}}},
				{"category": "Pizzas", "name": "Marguerita", "price": "R$ 42", "available": false},
				{"category": "Bebidas", "name": "Coca 2L", "price": "doze"},
				{"category": "Bebidas", "name": "Suco"},
			}
			var pv importMenuRowsOutput
			ok(t, cs, "import_menu_rows", map[string]any{"rows": rows}, &pv)
			if !pv.DryRun || pv.Valid != 3 || pv.Invalid != 1 || len(pv.Rows) != 4 || !strings.Contains(pv.Note, "nothing was saved") {
				t.Fatalf("preview = %+v", pv)
			}
			if pv.Rows[0].PriceCents != 4590 || pv.Rows[0].Price != "R$ 45,90" || pv.Rows[0].Options[0].Choices[1].PriceDeltaCents != 1000 ||
				pv.Rows[1].Available || pv.Rows[1].PriceCents != 4200 {
				t.Fatalf("parsed rows = %+v", pv.Rows)
			}
			if len(pv.Rows[2].Errors) == 0 || !strings.Contains(pv.Rows[2].Errors[0], "não reconhecido") {
				t.Fatalf("bad price row = %+v", pv.Rows[2])
			}
			if len(pv.Rows[3].Warnings) == 0 || !strings.Contains(pv.Rows[3].Warnings[0], "sem preço") {
				t.Fatalf("missing price row = %+v", pv.Rows[3])
			}
			var cur menuOutput
			ok(t, cs, "get_menu", nil, &cur)
			if cur.ItemCount != 0 {
				t.Fatalf("the preview saved %d items", cur.ItemCount)
			}
			// A save with errors is refused, and saves nothing.
			fails(t, cs, "import_menu_rows", map[string]any{"rows": rows, "dry_run": false}, "nothing saved: 1 rows have errors")
			ok(t, cs, "get_menu", nil, &cur)
			if cur.ItemCount != 0 {
				t.Fatalf("a refused save left %d items", cur.ItemCount)
			}
			// skip_invalid saves the others.
			var saved importMenuRowsOutput
			ok(t, cs, "import_menu_rows", map[string]any{"rows": rows, "dry_run": false, "skip_invalid": true}, &saved)
			if saved.DryRun || saved.ItemsCreated != 3 || saved.CategoriesCreated != 2 || saved.ItemsInMenu != 3 || !strings.Contains(saved.Note, "Do not call it again") {
				t.Fatalf("saved = %+v", saved)
			}
			ok(t, cs, "get_menu", nil, &cur)
			if cur.ItemCount != 3 || len(cur.Categories) != 2 || cur.Categories[0].Name != "Pizzas" || len(cur.Categories[0].Items[0].Options) != 1 {
				t.Fatalf("menu = %+v", cur)
			}
			// Previewing the same rows again warns that saving duplicates them.
			ok(t, cs, "import_menu_rows", map[string]any{"rows": rows[:1]}, &pv)
			if pv.ItemsInMenu != 3 || len(pv.Rows[0].Warnings) == 0 || !strings.Contains(pv.Rows[0].Warnings[0], "já existe") {
				t.Fatalf("duplicate preview = %+v", pv)
			}
			// replace empties the menu first.
			ok(t, cs, "import_menu_rows", map[string]any{"rows": rows[:1], "dry_run": false, "replace": true}, &saved)
			ok(t, cs, "get_menu", nil, &cur)
			if cur.ItemCount != 1 {
				t.Fatalf("after replace = %+v", cur)
			}
			// Input checks.
			fails(t, cs, "import_menu_rows", map[string]any{"rows": []map[string]any{}}, "validating")
			fails(t, cs, "import_menu_rows", map[string]any{"rows": []map[string]any{{"name": "X",
				"options": []map[string]any{{"name": "T", "choices": []map[string]any{{"name": "a", "price_delta": "muito"}}}}}}}, "price_delta")
		})
	}
}

// The item limit is checked in the preview against the current menu, and a save over it
// is refused.
func TestImportMenuRowsLimit(t *testing.T) {
	e := newEnv(t, nil)
	id, tok := e.connected("LIM")
	cs := e.client(modeLocal, tok)
	fill := make([]menu.Row, store.MaxMenuItems-1)
	for i := range fill {
		fill[i] = menu.Row{Name: fmt.Sprint("Item ", i), PriceCents: 100, Available: true}
	}
	if _, err := e.menu.ImportRows(context.Background(), id, service.MenuRowsInput{Rows: fill}); err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{{"name": "A", "price": "1"}, {"name": "B", "price": "2"}}
	var pv importMenuRowsOutput
	ok(t, cs, "import_menu_rows", map[string]any{"rows": rows}, &pv)
	if pv.ItemsInMenu != store.MaxMenuItems-1 || len(pv.Errors) != 1 || !strings.Contains(pv.Errors[0], "limit") {
		t.Fatalf("preview = %+v", pv)
	}
	fails(t, cs, "import_menu_rows", map[string]any{"rows": rows, "dry_run": false}, "menu_limit")
	ok(t, cs, "import_menu_rows", map[string]any{"rows": rows[:1], "dry_run": false}, &pv)
	fails(t, cs, "create_menu_item", map[string]any{"name": "C", "price": "3"}, "menu_limit")
	big := make([]map[string]any, store.MaxMenuItems+1)
	for i := range big {
		big[i] = map[string]any{"name": "x"}
	}
	fails(t, cs, "import_menu_rows", map[string]any{"rows": big}, "validating")
}

func pngServer(t *testing.T) *httptest.Server {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(buf.Bytes())
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The one-item tools: categories by name, prices as text, options, availability, photo,
// order and the destructive deletes.
func TestMenuEditTools(t *testing.T) {
	img := pngServer(t)
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			_, tok := e.connected("ED")
			cs := e.client(m, tok)

			fails(t, cs, "create_menu_item", map[string]any{"name": "Calabresa", "price": "45,90", "category": "Pizzas"}, "pass create_category=true")
			var it menuItemFull
			ok(t, cs, "create_menu_item", map[string]any{"name": "Calabresa", "price": "45,90", "category": "pizzas", "create_category": true,
				"tags": []string{"Picante"}}, &it)
			if it.PriceCents != 4590 || it.CategoryID == "" || it.Tags[0] != "picante" || !it.Available {
				t.Fatalf("created = %+v", it)
			}
			var it2 menuItemFull
			// The category name matches ignoring case; no price: a note says so.
			ok(t, cs, "create_menu_item", map[string]any{"name": "Marguerita", "category": "PIZZAS"}, &it2)
			if it2.CategoryID != it.CategoryID || !strings.Contains(it2.Note, "without a price") {
				t.Fatalf("second = %+v", it2)
			}
			fails(t, cs, "create_menu_item", map[string]any{"name": "X", "price": "abc"}, "price must be")

			var upd menuItemFull
			ok(t, cs, "update_menu_item", map[string]any{"item_id": it2.ID, "price": "R$ 42,00", "description": "Molho e queijo"}, &upd)
			if upd.PriceCents != 4200 || upd.Description != "Molho e queijo" || upd.Name != "Marguerita" {
				t.Fatalf("updated = %+v", upd)
			}
			ok(t, cs, "update_menu_item", map[string]any{"item_id": it2.ID, "category": "Doces", "create_category": true}, &upd)
			if upd.CategoryID == it.CategoryID {
				t.Fatalf("moved = %+v", upd)
			}

			var opt menuOptionOut
			ok(t, cs, "add_menu_option_group", map[string]any{"item_id": it.ID, "name": "Tamanho", "required": true,
				"choices": []map[string]any{{"name": "P"}, {"name": "G", "price_delta": "+10,00"}}}, &opt)
			if opt.ID == "" || opt.Min != 1 || opt.Choices[1].PriceDeltaCents != 1000 {
				t.Fatalf("option = %+v", opt)
			}
			var one menuItemOut
			ok(t, cs, "get_menu_item", map[string]any{"item_id": it.ID}, &one)
			if len(one.Options) != 1 || one.Options[0].ID != opt.ID {
				t.Fatalf("get_menu_item options = %+v", one.Options)
			}
			ok(t, cs, "update_menu_option_group", map[string]any{"item_id": it.ID, "option_id": opt.ID, "name": "Tamanho",
				"choices": []map[string]any{{"name": "M"}, {"name": "G", "price_delta_cents": 800}}}, &opt)
			if opt.Choices[0].Name != "M" || opt.Required {
				t.Fatalf("updated option = %+v", opt)
			}
			fails(t, cs, "add_menu_option_group", map[string]any{"item_id": it.ID, "name": "Vazio", "choices": []map[string]any{}}, "validating")
			var del menuDeleted
			ok(t, cs, "delete_menu_option_group", map[string]any{"item_id": it.ID, "option_id": opt.ID}, &del)
			fails(t, cs, "delete_menu_option_group", map[string]any{"item_id": it.ID, "option_id": opt.ID}, "not_found")

			ok(t, cs, "set_menu_item_available", map[string]any{"item_id": it.ID, "available": false}, &upd)
			if upd.Available || !strings.Contains(upd.Note, "esgotado") {
				t.Fatalf("sold out = %+v", upd)
			}
			ok(t, cs, "set_menu_item_image_url", map[string]any{"item_id": it.ID, "url": img.URL + "/foto.png"}, &upd)
			if !upd.HasPhoto {
				t.Fatalf("photo = %+v", upd)
			}

			var c menuCategoryResult
			ok(t, cs, "create_menu_category", map[string]any{"name": "Bebidas"}, &c)
			ok(t, cs, "update_menu_category", map[string]any{"category_id": c.ID, "name": "Bebidas geladas"}, &c)
			if c.Name != "Bebidas geladas" {
				t.Fatalf("renamed = %+v", c)
			}
			var mv menuOutput
			ok(t, cs, "get_menu", nil, &mv)
			ids := []string{}
			for i := len(mv.Categories) - 1; i >= 0; i-- {
				ids = append(ids, mv.Categories[i].ID)
			}
			ids = append(ids, c.ID) // the empty category is not listed by get_menu
			var ro menuReordered
			ok(t, cs, "reorder_menu", map[string]any{"categories": ids}, &ro)
			fails(t, cs, "reorder_menu", map[string]any{"items": []string{it.ID}}, "category_id is required")
			ok(t, cs, "reorder_menu", map[string]any{"items": []string{it.ID}, "category_id": ""}, &ro)
			ok(t, cs, "get_menu_item", map[string]any{"item_id": it.ID}, &one)

			ok(t, cs, "delete_menu_category", map[string]any{"category_id": c.ID}, &del)
			ok(t, cs, "delete_menu_item", map[string]any{"item_id": it.ID}, &del)
			fails(t, cs, "get_menu_item", map[string]any{"item_id": it.ID}, "not_found")
			fails(t, cs, "delete_menu_item", map[string]any{"item_id": "nope"}, "item")
		})
	}
}

// Destructive tools carry the annotation and say "only when the user asks"; the editing
// tools are served by MCP but flagged assistant-only, so the agents' table has none.
func TestMenuWriteToolAnnotations(t *testing.T) {
	e := newEnv(t, nil)
	_, tok := e.connected("ANN")
	cs := e.client(modeLocal, tok)
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	destructive := map[string]bool{"delete_menu_item": true, "delete_menu_category": true, "delete_menu_option_group": true, "import_menu_rows": true}
	seen := 0
	for _, tool := range listed.Tools {
		want, ok := destructive[tool.Name]
		if !ok && !strings.Contains(tool.Name, "menu") {
			continue
		}
		got := tool.Annotations != nil && tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint
		if got != want {
			t.Fatalf("%s destructive = %v", tool.Name, got)
		}
		if want {
			seen++
			if !strings.Contains(tool.Description, "only when the user") && !strings.Contains(tool.Description, "Only when the user") {
				t.Fatalf("%s does not say only when the user asks: %s", tool.Name, tool.Description)
			}
		}
	}
	if seen != len(destructive) {
		t.Fatalf("saw %d destructive menu tools", seen)
	}
	desc := ""
	for _, tool := range listed.Tools {
		if tool.Name == "import_menu_rows" {
			desc = tool.Description
		}
	}
	for _, want := range []string{"dry_run=true", "ALWAYS", "Never invent prices", "only after the user confirms"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("import_menu_rows description lacks %q", want)
		}
	}
	if !strings.Contains(Instructions, "import_menu_rows with dry_run=true first") {
		t.Fatal("the server instructions do not explain the menu tools")
	}
	b := &Local{Caller: service.Scope{InstanceID: "inst_x"}}
	all, agent := Catalog(b, nil), AgentCatalog(b, nil)
	n := 0
	for _, d := range all {
		if d.AssistantOnly {
			n++
		}
	}
	if n != 13 || len(agent) != len(all)-n {
		t.Fatalf("assistant-only = %d, agents %d of %d", n, len(agent), len(all))
	}
	for _, d := range agent {
		if d.AssistantOnly || (strings.Contains(d.Name, "menu") && !d.ReadOnly && !strings.HasPrefix(d.Name, "send_")) {
			t.Fatalf("agents get %s", d.Name)
		}
	}
}
