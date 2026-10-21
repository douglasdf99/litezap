package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestMenuCRUD(t *testing.T) {
	st := ordersStore(t)
	ctx := context.Background()
	pizzas, err := st.CreateMenuCategory(ctx, "i1", "Pizzas")
	if err != nil {
		t.Fatal(err)
	}
	drinks, _ := st.CreateMenuCategory(ctx, "i1", "Bebidas")
	if drinks.Position != 1 {
		t.Fatalf("position = %d", drinks.Position)
	}
	opts := []MenuOption{{Name: "Tamanho", Min: 1, Max: 1, Required: true, Choices: []MenuChoice{{Name: "P"}, {Name: "G", PriceDeltaCents: 1000}}}}
	cal, err := st.CreateMenuItem(ctx, "i1", MenuItem{CategoryID: pizzas.ID, Name: "Calabresa", PriceCents: 4590, Available: true,
		Tags: []string{"picante"}, Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	if cal.Currency != "BRL" || len(cal.Options) != 1 || cal.Options[0].Choices[1].PriceDeltaCents != 1000 || cal.Tags[0] != "picante" {
		t.Fatalf("item = %+v", cal)
	}
	mar, _ := st.CreateMenuItem(ctx, "i1", MenuItem{CategoryID: pizzas.ID, Name: "Marguerita", PriceCents: 4200, Available: true})
	coca, _ := st.CreateMenuItem(ctx, "i1", MenuItem{CategoryID: drinks.ID, Name: "Coca", PriceCents: 1200, Available: true})
	loose, _ := st.CreateMenuItem(ctx, "i1", MenuItem{Name: "Brinde", Available: true})
	if mar.Position != 1 || coca.Position != 0 {
		t.Fatalf("positions %d %d", mar.Position, coca.Position)
	}
	if _, err := st.CreateMenuItem(ctx, "i1", MenuItem{CategoryID: "mcat_nope", Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown category = %v", err)
	}
	// Order: categories by position, items by position, uncategorized last.
	if err := st.ReorderMenuCategories(ctx, "i1", []string{drinks.ID, pizzas.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReorderMenuItems(ctx, "i1", pizzas.ID, []string{mar.ID, cal.ID}); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListMenuItems(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, it := range list {
		got += it.Name + ","
	}
	if got != "Coca,Marguerita,Calabresa,Brinde," || len(list[2].Options) != 1 {
		t.Fatalf("order = %s", got)
	}
	// Move an item into another category by reordering it there.
	if err := st.ReorderMenuItems(ctx, "i1", drinks.ID, []string{coca.ID, loose.ID}); err != nil {
		t.Fatal(err)
	}
	if it, _ := st.GetMenuItem(ctx, "i1", loose.ID); it.CategoryID != drinks.ID || it.Position != 1 {
		t.Fatalf("moved = %+v", it)
	}
	if err := st.ReorderMenuItems(ctx, "i1", drinks.ID, []string{"mitm_x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id = %v", err)
	}
	// Update keeps options when nil, replaces them otherwise.
	cal.Name, cal.Options = "Calabresa especial", nil
	up, err := st.UpdateMenuItem(ctx, "i1", cal)
	if err != nil || up.Name != "Calabresa especial" || len(up.Options) != 1 {
		t.Fatalf("update = %+v, %v", up, err)
	}
	up.Options = []MenuOption{}
	if up, _ = st.UpdateMenuItem(ctx, "i1", up); len(up.Options) != 0 {
		t.Fatal("options not replaced")
	}
	if err := st.SetMenuItemAvailable(ctx, "i1", mar.ID, false); err != nil {
		t.Fatal(err)
	}
	if prev, err := st.SetMenuItemImage(ctx, "i1", mar.ID, "mimg_1"); err != nil || prev != "" {
		t.Fatal(prev, err)
	}
	if prev, _ := st.SetMenuItemImage(ctx, "i1", mar.ID, "mimg_2"); prev != "mimg_1" {
		t.Fatalf("prev = %s", prev)
	}
	// Option groups.
	o, err := st.AddMenuOption(ctx, "i1", mar.ID, MenuOption{Name: "Borda", Max: 1, Choices: []MenuChoice{{Name: "Catupiry", PriceDeltaCents: 800}}})
	if err != nil {
		t.Fatal(err)
	}
	o.Name = "Borda recheada"
	if err := st.UpdateMenuOption(ctx, "i1", mar.ID, o); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateMenuOption(ctx, "i1", coca.ID, o); !errors.Is(err, ErrNotFound) {
		t.Fatal("option updated through another item")
	}
	if it, _ := st.GetMenuItem(ctx, "i1", mar.ID); it.Available || it.ImageID != "mimg_2" || it.Options[0].Name != "Borda recheada" {
		t.Fatalf("item = %+v", it)
	}
	// Deleting a category keeps its items.
	if err := st.DeleteMenuCategory(ctx, "i1", pizzas.ID); err != nil {
		t.Fatal(err)
	}
	if it, _ := st.GetMenuItem(ctx, "i1", mar.ID); it.CategoryID != "" {
		t.Fatal("item kept its deleted category")
	}
	img, err := st.DeleteMenuItem(ctx, "i1", mar.ID)
	if err != nil || img != "mimg_2" {
		t.Fatal(img, err)
	}
	if _, err := st.GetMenuItem(ctx, "i1", mar.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("item not deleted")
	}
	// Another instance never sees this menu.
	if _, err := st.GetMenuItem(ctx, "i2", coca.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-instance read")
	}
}

func TestMenuLimits(t *testing.T) {
	st := ordersStore(t)
	ctx := context.Background()
	for i := 0; i < MaxMenuCategories; i++ {
		if _, err := st.CreateMenuCategory(ctx, "i1", fmt.Sprint("c", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateMenuCategory(ctx, "i1", "over"); !errors.Is(err, ErrMenuLimit) {
		t.Fatalf("category over limit = %v", err)
	}
	// Concurrent creations cannot pass the item limit.
	var wg sync.WaitGroup
	var mu sync.Mutex
	limited := 0
	for i := 0; i < MaxMenuItems+20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.CreateMenuItem(ctx, "i1", MenuItem{Name: fmt.Sprint("item", i), Available: true})
			if errors.Is(err, ErrMenuLimit) {
				mu.Lock()
				limited++
				mu.Unlock()
			} else if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if _, n, _ := st.MenuCounts(ctx, "i1"); n != MaxMenuItems || limited != 20 {
		t.Fatalf("items = %d, limited = %d", n, limited)
	}
	many := make([]MenuOption, MaxMenuOptions+1)
	for i := range many {
		many[i] = MenuOption{Name: fmt.Sprint("o", i), Max: 1, Choices: []MenuChoice{{Name: "a"}}}
	}
	list, _ := st.ListMenuItems(ctx, "i1")
	list[0].Options = many
	if _, err := st.UpdateMenuItem(ctx, "i1", list[0]); !errors.Is(err, ErrMenuLimit) {
		t.Fatalf("options over limit = %v", err)
	}
}

func TestImportMenu(t *testing.T) {
	st := ordersStore(t)
	ctx := context.Background()
	old, _ := st.CreateMenuItem(ctx, "i1", MenuItem{Name: "Antigo", ImageID: "mimg_old", Available: true})
	_ = old
	pizzas, _ := st.CreateMenuCategory(ctx, "i1", "Pizzas")
	res, err := st.ImportMenu(ctx, "i1", false, []MenuImportItem{
		{Category: "pizzas", Item: MenuItem{Name: "Calabresa", PriceCents: 4500, Available: true}},
		{Category: "Bebidas", Item: MenuItem{Name: "Coca", PriceCents: 1200, Available: true, SKU: "wa:1"}},
		{Category: "BEBIDAS", Item: MenuItem{Name: "Suco", Available: true}},
		{Item: MenuItem{Name: "Sem categoria", Available: true}},
	})
	if err != nil || res.ItemsCreated != 4 || res.CategoriesCreated != 1 || len(res.ItemIDs) != 4 {
		t.Fatalf("import = %+v, %v", res, err)
	}
	if it, _ := st.GetMenuItem(ctx, "i1", res.ItemIDs[0]); it.CategoryID != pizzas.ID {
		t.Fatal("existing category not matched by name")
	}
	if it, err := st.MenuItemBySKU(ctx, "i1", "wa:1"); err != nil || it.Name != "Coca" {
		t.Fatal(it, err)
	}
	// Replace empties the menu first and reports the removed images.
	res, err = st.ImportMenu(ctx, "i1", true, []MenuImportItem{{Category: "Novos", Item: MenuItem{Name: "Único", Available: true}}})
	if err != nil || len(res.RemovedImages) != 1 || res.RemovedImages[0] != "mimg_old" {
		t.Fatalf("replace = %+v, %v", res, err)
	}
	cats, items, _ := st.MenuCounts(ctx, "i1")
	if cats != 1 || items != 1 {
		t.Fatalf("after replace: %d categories, %d items", cats, items)
	}
	// Over the limit, nothing is imported.
	big := make([]MenuImportItem, MaxMenuItems)
	for i := range big {
		big[i] = MenuImportItem{Item: MenuItem{Name: fmt.Sprint("x", i), Available: true}}
	}
	if _, err := st.ImportMenu(ctx, "i1", false, big); !errors.Is(err, ErrMenuLimit) {
		t.Fatalf("over limit = %v", err)
	}
	if _, items, _ := st.MenuCounts(ctx, "i1"); items != 1 {
		t.Fatalf("partial import kept: %d", items)
	}
}
