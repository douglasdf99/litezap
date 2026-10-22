package menu

import (
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/store"
)

func TestSaleValidationAndEffectivePrice(t *testing.T) {
	ok := Row{Name: "Pizza", PriceCents: 5000, SalePriceCents: 3990, SaleUntil: "31/12/2026", Available: true}
	NormalizeRow(&ok)
	if ok.SaleUntil != "2026-12-31" || len(ValidateRow(ok)) != 0 {
		t.Fatalf("valid sale = %+v %v", ok, ValidateRow(ok))
	}
	for _, r := range []Row{
		{Name: "x", PriceCents: 5000, SalePriceCents: 5000},                    // not lower
		{Name: "x", PriceCents: 5000, SalePriceCents: 6000},                    // higher
		{Name: "x", PriceCents: 5000, SalePriceCents: -1},                      // negative
		{Name: "x", PriceCents: 5000, SalePriceCents: 4000, SaleUntil: "logo"}, // bad date
		{Name: "x", PriceCents: 5000, SaleUntil: "2026-12-31"},                 // date without sale
		{Name: "x", LinkURL: "javascript:alert(1)"},                            // not http(s)
		{Name: "x", LinkURL: "ftp://x.com/a"},
		{Name: "x", ImageURLs: []string{"https://a/1", "https://a/2", "https://a/3", "https://a/4", "https://a/5", "https://a/6",
			"https://a/7", "https://a/8", "https://a/9", "https://a/10", "https://a/11"}}, // 11 photos
	} {
		NormalizeRow(&r)
		if len(ValidateRow(r)) == 0 {
			t.Fatalf("accepted %+v", r)
		}
	}
	ps := ValidateRowProblems(Row{Name: "x", PriceCents: 5000, SalePriceCents: 5000})
	if len(ps) != 1 || ps[0].Field != "preco_promocional" || !strings.Contains(ps[0].Message, "menor que o preço") {
		t.Fatalf("problems = %+v", ps)
	}
	it := store.MenuItem{Name: "Pizza", PriceCents: 5000, SalePriceCents: 3990, SaleUntil: "2026-12-31", Currency: "BRL", Available: true}
	if EffectivePrice(it, "2026-12-31") != 3990 || !OnSale(it, "2026-12-31") || EffectivePrice(it, "2027-01-01") != 5000 || OnSale(it, "2027-01-01") {
		t.Fatal("sale end date not applied")
	}
	if got := PriceLabel(it, "2026-10-01"); got != "~R$ 50,00~ *R$ 39,90*" {
		t.Fatalf("label = %q", got)
	}
	if got := CaptionAt(it, "2026-10-01"); !strings.HasPrefix(got, "*Pizza* — ~R$ 50,00~ *R$ 39,90*") {
		t.Fatalf("caption = %q", got)
	}
	if got := CaptionAt(it, "2027-01-02"); got != "*Pizza* — R$ 50,00" {
		t.Fatalf("caption after the sale = %q", got)
	}
	it.LinkURL = "https://loja.example/pizza"
	if got := CaptionAt(it, "2027-01-02"); !strings.HasSuffix(got, "\nhttps://loja.example/pizza") {
		t.Fatalf("caption link = %q", got)
	}
}

func TestFormatHidesHiddenAndCollections(t *testing.T) {
	cats := []store.MenuCategory{{ID: "c1", Name: "Pizzas"}}
	items := []store.MenuItem{
		{ID: "a", CategoryID: "c1", Name: "Calabresa", PriceCents: 5000, SalePriceCents: 3990, Available: true},
		{ID: "b", CategoryID: "c1", Name: "Secreta", PriceCents: 100, Available: true, Hidden: true},
		{ID: "c", CategoryID: "c1", Name: "Marguerita", PriceCents: 4200, Available: false},
	}
	text := strings.Join(Format(cats, items, FormatOptions{Today: "2026-01-01", IncludeUnavailable: true}), "\n")
	if strings.Contains(text, "Secreta") || !strings.Contains(text, "• Calabresa — ~R$ 50,00~ *R$ 39,90*") || !strings.Contains(text, "Marguerita — R$ 42,00 (esgotado)") {
		t.Fatalf("text = %q", text)
	}
	c := store.MenuCollection{ID: "col", Name: "Promoções", Description: "Só hoje", ItemIDs: []string{"c", "b", "a"}}
	chunks := FormatCollection(c, items, CollectionOptions{Today: "2026-01-01"})
	if len(chunks) != 1 || !strings.HasPrefix(chunks[0], "*Promoções*\nSó hoje\n\n• Calabresa") || strings.Contains(chunks[0], "Secreta") ||
		strings.Contains(chunks[0], "Marguerita") {
		t.Fatalf("collection = %q", chunks)
	}
	if FormatCollection(store.MenuCollection{Name: "X", ItemIDs: []string{"b"}}, items, CollectionOptions{}) != nil {
		t.Fatal("a collection with only hidden items has text")
	}
}

func TestOrderEngine(t *testing.T) {
	opts := []store.MenuOption{
		{Name: "Tamanho", Required: true, Min: 1, Max: 1, Choices: []store.MenuChoice{{Name: "P"}, {Name: "G", PriceDeltaCents: 1000}}},
		{Name: "Borda", Max: 1, Choices: []store.MenuChoice{{Name: "Catupiry", PriceDeltaCents: 800}}},
		{Name: "Adicionais", Max: 2, Choices: []store.MenuChoice{{Name: "Bacon", PriceDeltaCents: 450}, {Name: "Ovo", PriceDeltaCents: 200},
			{Name: "Milho", PriceDeltaCents: 150}}},
	}
	items := []store.MenuItem{
		{ID: "mitm_a", Name: "Calabresa", PriceCents: 5000, SalePriceCents: 3990, Currency: "BRL", Available: true, Options: opts},
		{ID: "mitm_b", Name: "Coca 2L", PriceCents: 1200, Currency: "BRL", Available: true},
		{ID: "mitm_c", Name: "Marguerita", PriceCents: 4200, Currency: "BRL", Available: false},
		{ID: "mitm_d", Name: "Secreta", PriceCents: 100, Currency: "BRL", Available: true, Hidden: true},
	}
	r := PriceOrder(items, []OrderLineInput{
		{ItemID: "mitm_a", Quantity: 2, Options: []string{"G", "Borda: Catupiry", "bacon"}},
		{Name: "coca 2l", Quantity: 3},
	}, "2026-01-01")
	// (39,90 sale + 10 + 8 + 4,50) x 2 = 124,80; 3 x 12 = 36; regular (50+22,50) x 2 = 145.
	if !r.Valid || len(r.Problems) != 0 || r.Lines[0].UnitPriceCents != 6240 || r.Lines[0].SubtotalCents != 12480 ||
		r.Lines[1].SubtotalCents != 3600 || r.TotalCents != 16080 || r.SavingsCents != 2020 || !r.Lines[0].OnSale ||
		r.Lines[0].BasePriceCents != 3990 || r.Lines[0].RegularPriceCents != 5000 || len(r.Lines[0].Options) != 3 {
		t.Fatalf("order = %+v", r)
	}
	// After the sale (sale_until passed) the regular price applies.
	items[0].SaleUntil = "2025-12-31"
	r2 := PriceOrder(items, []OrderLineInput{{ItemID: "mitm_a", Quantity: 1, Options: []string{"P"}}}, "2026-01-01")
	if r2.TotalCents != 5000 || r2.Lines[0].OnSale || r2.SavingsCents != 0 {
		t.Fatalf("after sale = %+v", r2)
	}
	items[0].SaleUntil = ""
	// Problems: missing required, unknown option, too many choices, sold out, hidden,
	// unknown item, bad quantity.
	bad := PriceOrder(items, []OrderLineInput{
		{ItemID: "mitm_a", Quantity: 1, Options: []string{"Gigante"}},
		{ItemID: "mitm_a", Quantity: 1, Options: []string{"P", "G"}},
		{ItemID: "mitm_a", Quantity: 1, Options: []string{"P", "Bacon", "Ovo", "Milho"}},
		{ItemID: "mitm_c", Quantity: 1},
		{ItemID: "mitm_d", Quantity: 1},
		{Name: "Coxinha", Quantity: 1, FallbackUnitCents: 600},
		{ItemID: "mitm_b", Quantity: 0},
	}, "2026-01-01")
	if bad.Valid || len(bad.Lines) != 7 {
		t.Fatalf("bad = %+v", bad)
	}
	want := []string{"escolha Tamanho", "passa do máximo", "passa do máximo", "esgotado", "não encontrado", "não encontrado", "quantidade"}
	for i, w := range want {
		if !strings.Contains(strings.Join(bad.Lines[i].Problems, "; "), w) {
			t.Fatalf("line %d problems %v, want %q", i+1, bad.Lines[i].Problems, w)
		}
	}
	if bad.Lines[4].InMenu || bad.Lines[4].Name == "Secreta" || bad.Lines[5].SubtotalCents != 600 || !strings.HasPrefix(bad.Problems[0], "item 1 (Calabresa)") {
		t.Fatalf("hidden/unknown lines = %+v / %+v / %v", bad.Lines[4], bad.Lines[5], bad.Problems)
	}
	// The summary shows options, the sale struck through, the total and the savings.
	s := OrderSummary(r, OrderInfo{CustomerName: "Ana", Address: "Rua B, 5"})
	for _, w := range []string{"*Resumo do pedido*", "2x Calabresa — R$ 124,80", "Tamanho: G (+R$ 10,00) · Borda: Catupiry (+R$ 8,00) · Adicionais: Bacon (+R$ 4,50)",
		"promoção: ~R$ 50,00~ *R$ 39,90* cada", "3x Coca 2L — R$ 36,00", "*Total: R$ 160,80*", "Você economiza R$ 20,20", "Cliente: Ana", "Entrega: Rua B, 5"} {
		if !strings.Contains(s, w) {
			t.Fatalf("summary lacks %q:\n%s", w, s)
		}
	}
	// A stored draft keeps its lines and recounts the same totals.
	back := FromDraft(DraftRow(r, "5511@s.whatsapp.net", "mcp", OrderInfo{}))
	if back.TotalCents != r.TotalCents || back.SavingsCents != r.SavingsCents || !back.Valid || len(back.Lines) != 2 {
		t.Fatalf("recount = %+v", back)
	}
	if FormatQuantity(1.5) != "1,5" || FormatQuantity(2) != "2" {
		t.Fatal("quantity format")
	}
}

func TestCSVExportRoundTripAndFormulaEscape(t *testing.T) {
	cats := []store.MenuCategory{{ID: "c1", Name: "Pizzas"}}
	colls := []store.MenuCollection{{ID: "k1", Name: "Promoções"}, {ID: "k2", Name: "Mais vendidos"}}
	items := []store.MenuItem{
		{ID: "a", CategoryID: "c1", Name: "Calabresa", Description: "Molho; calabresa \"fina\"", PriceCents: 123450, SalePriceCents: 99990,
			SaleUntil: "2026-12-31", Available: true, SKU: "CAL-1", LinkURL: "https://loja.example/cal", Tags: []string{"picante", "carne"},
			CollectionIDs: []string{"k1", "k2"}, Options: []store.MenuOption{
				{Name: "Tamanho", Required: true, Min: 1, Max: 1, Choices: []store.MenuChoice{{Name: "P"}, {Name: "G", PriceDeltaCents: 1000}}},
				{Name: "Adicionais", Max: 3, Choices: []store.MenuChoice{{Name: "Bacon", PriceDeltaCents: 450}, {Name: "Sem cebola", PriceDeltaCents: -200}}},
				{Name: "Bebida", Required: true, Min: 1, Max: 1, Choices: []store.MenuChoice{{Name: "Lata 350"}, {Name: "Garrafa 600", PriceDeltaCents: 300}}},
			},
			Images: []store.MenuImage{{ID: "m1", SourceURL: "https://cdn.example/1.jpg"}, {ID: "m2"}, {ID: "m3", SourceURL: "https://cdn.example/3.jpg"}}},
		{ID: "b", Name: "=HYPERLINK(\"http://evil\",\"x\")", Description: "+1 grátis", PriceCents: 1000, Available: false, Hidden: true,
			SKU: "@sku", Tags: []string{"-promo"}},
	}
	out := string(ExportCSV(cats, items, colls, func(im store.MenuImage) string { return im.SourceURL }))
	if !strings.HasPrefix(out, "\ufeffcategoria;nome;descricao;preco;preco_promocional;promocao_ate;disponivel;oculto;sku;link;tags;colecoes;opcoes;fotos\n") {
		t.Fatalf("header = %q", out[:200])
	}
	// No cell starts with a formula character.
	for _, bad := range []string{";=HYPERLINK", ";+1 grátis", ";@sku", ";-promo", "\n=", "\n+", "\n-", "\n@"} {
		if strings.Contains(out, bad) {
			t.Fatalf("unescaped formula %q in:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, `'=HYPERLINK`) || !strings.Contains(out, ";'+1 grátis;") || !strings.Contains(out, ";'@sku;") {
		t.Fatalf("escaped cells missing:\n%s", out)
	}
	// Round trip: the export imports back to the same items.
	res := ParseCSV(out)
	if len(res.Errors) != 0 || len(res.Rows) != 2 {
		t.Fatalf("reimport = %+v", res)
	}
	a, b := res.Rows[0], res.Rows[1]
	if len(a.Errors) != 0 || a.Category != "Pizzas" || a.Name != "Calabresa" || a.Description != "Molho; calabresa \"fina\"" || a.PriceCents != 123450 ||
		a.SalePriceCents != 99990 || a.SaleUntil != "2026-12-31" || !a.Available || a.Hidden || a.SKU != "CAL-1" || a.LinkURL != "https://loja.example/cal" ||
		strings.Join(a.Tags, ",") != "picante,carne" || strings.Join(a.Collections, "|") != "Promoções|Mais vendidos" ||
		strings.Join(a.ImageURLs, " ") != "https://cdn.example/1.jpg https://cdn.example/3.jpg" {
		t.Fatalf("row a = %+v", a)
	}
	if len(a.Options) != 3 || a.Options[0].Name != "Tamanho" || !a.Options[0].Required || a.Options[0].Choices[1].PriceDeltaCents != 1000 ||
		a.Options[1].Required || a.Options[1].Max != 2 || a.Options[1].Choices[1].PriceDeltaCents != -200 ||
		a.Options[2].Choices[0].Name != "Lata 350" || a.Options[2].Choices[0].PriceDeltaCents != 0 || a.Options[2].Choices[1].PriceDeltaCents != 300 {
		t.Fatalf("options a = %+v", a.Options)
	}
	if b.Name != `=HYPERLINK("http://evil","x")` || b.Description != "+1 grátis" || b.SKU != "@sku" || b.Available || !b.Hidden ||
		strings.Join(b.Tags, ",") != "-promo" {
		t.Fatalf("row b (unescaped back) = %+v", b)
	}
	if EscapeCell("Pizza") != "Pizza" || EscapeCell("-5") != "'-5" || EscapeCell("\tx") != "'\tx" || unescapeCell("'Pizza") != "'Pizza" {
		t.Fatal("escape rules")
	}
}

func TestParseCSVNewColumns(t *testing.T) {
	res := ParseCSV("nome;preco;preco_promocional;promocao_ate;link;colecoes;fotos;oculto\n" +
		"Pizza;50;39,90;31/12/2026;https://x.example/p;Promoções|Mais vendidos;https://a.example/1.jpg|https://a.example/2.jpg;sim\n" +
		"Suco;10;abc;;;;;talvez\n" +
		"Coca;10;12;;;;;\n")
	if len(res.Rows) != 3 {
		t.Fatalf("rows = %+v", res)
	}
	p := res.Rows[0]
	if len(p.Errors) != 0 || p.SalePriceCents != 3990 || p.SaleUntil != "2026-12-31" || p.LinkURL != "https://x.example/p" || !p.Hidden ||
		len(p.Collections) != 2 || len(p.ImageURLs) != 2 || p.ImageURL != "https://a.example/1.jpg" || p.SalePriceText != "R$ 39,90" {
		t.Fatalf("row 1 = %+v", p)
	}
	s := res.Rows[1]
	if len(s.Problems) != 2 || s.Problems[0].Field != "preco_promocional" || s.Problems[0].Line != 3 ||
		!strings.Contains(s.Problems[0].Message, "preço promocional inválido 'abc'") || s.Problems[1].Field != "oculto" {
		t.Fatalf("row 2 = %+v", s.Problems)
	}
	if c := res.Rows[2]; len(c.Problems) != 1 || c.Problems[0].Field != "preco_promocional" {
		t.Fatalf("row 3 = %+v", c.Problems)
	}
}
