package menu

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/douglasdf99/litezap/internal/store"
)

// The order engine prices a cart from the Cardápio: each line's unit price is the
// item's effective price (its sale price while the sale lasts) plus the price
// differences of the chosen options, whatever price the caller (an assistant, the
// attendant's model) wrote. It checks availability, hidden items and the option rules.
// The REST API, the MCP tools and the attendant's agent.order_draft all use it.

// Order limits.
const (
	MaxOrderLines    = 50
	MaxOrderQuantity = 999
	MaxOrderNotes    = 500
)

// OrderLineInput is one line of a cart: an item by id (or by name, as the attendant's
// model writes it), a quantity and the chosen choices by name ("G", or "Tamanho: G"
// when two groups have a choice with the same name).
type OrderLineInput struct {
	ItemID   string   `json:"item_id,omitempty"`
	Name     string   `json:"name,omitempty"`
	Quantity float64  `json:"quantity"`
	Options  []string `json:"options,omitempty"`
	Notes    string   `json:"notes,omitempty"`
	// FallbackUnitCents prices a line whose item is not in the menu (the attendant's
	// model price, flagged as out of the menu); never used for menu items.
	FallbackUnitCents int64 `json:"-"`
}

// OrderChoice is a chosen option.
type OrderChoice struct {
	Group           string `json:"group"`
	Choice          string `json:"choice"`
	PriceDeltaCents int64  `json:"price_delta_cents"`
}

// OrderLine is a priced line.
type OrderLine struct {
	ItemID    string  `json:"item_id,omitempty"`
	Name      string  `json:"name"`
	Quantity  float64 `json:"quantity"`
	InMenu    bool    `json:"in_menu"`
	Available bool    `json:"available"`
	// RegularPriceCents is the item's price; BasePriceCents its effective price (the
	// sale price during a sale); UnitPriceCents adds the options' differences.
	RegularPriceCents int64         `json:"regular_price_cents"`
	BasePriceCents    int64         `json:"base_price_cents"`
	OnSale            bool          `json:"on_sale"`
	Options           []OrderChoice `json:"options"`
	UnitPriceCents    int64         `json:"unit_price_cents"`
	SubtotalCents     int64         `json:"subtotal_cents"`
	Notes             string        `json:"notes,omitempty"`
	UnknownOptions    []string      `json:"unknown_options,omitempty"`
	MissingOptions    []string      `json:"missing_required_options,omitempty"`
	// Problems say why the line cannot be ordered as it is (Portuguese).
	Problems []string `json:"problems,omitempty"`
}

// OrderResult is a priced cart.
type OrderResult struct {
	Lines         []OrderLine `json:"lines"`
	SubtotalCents int64       `json:"subtotal_cents"`
	TotalCents    int64       `json:"total_cents"`
	// SavingsCents is what the sale prices take off the regular prices.
	SavingsCents int64  `json:"savings_cents"`
	Currency     string `json:"currency"`
	// Valid is false when a line has a problem; Problems lists them ("item 2: ...").
	Valid    bool     `json:"valid"`
	Problems []string `json:"problems"`
}

// FoldName lowercases and drops accents and extra spaces, to match names.
func FoldName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.Join(strings.Fields(s), " ")) {
		b.WriteString(fold(r))
	}
	return b.String()
}

// PriceOrder prices the lines against the menu's items on day today. Hidden items are
// treated as not in the menu.
func PriceOrder(items []store.MenuItem, in []OrderLineInput, today string) OrderResult {
	byID := map[string]store.MenuItem{}
	byName := map[string]store.MenuItem{}
	for _, it := range items {
		if it.Hidden {
			continue
		}
		byID[it.ID] = it
		if _, ok := byName[FoldName(it.Name)]; !ok {
			byName[FoldName(it.Name)] = it
		}
	}
	var extra []string
	if len(in) == 0 {
		extra = append(extra, "o pedido não tem itens")
	}
	if len(in) > MaxOrderLines {
		extra = append(extra, fmt.Sprintf("no máximo %d itens por pedido", MaxOrderLines))
		in = in[:MaxOrderLines]
	}
	lines := make([]OrderLine, 0, len(in))
	currency := "BRL"
	for _, li := range in {
		line := priceLine(byID, byName, li, today)
		if line.InMenu {
			currency = currencyOf(byID[line.ItemID])
		}
		lines = append(lines, line)
	}
	res := Totals(lines, currency)
	if len(extra) > 0 {
		res.Valid, res.Problems = false, append(extra, res.Problems...)
	}
	return res
}

// Totals recomputes a cart's subtotal, total, savings and problems from its priced
// lines (a stored draft keeps only its lines).
func Totals(lines []OrderLine, currency string) OrderResult {
	if currency == "" {
		currency = "BRL"
	}
	res := OrderResult{Lines: lines, Currency: currency, Valid: len(lines) > 0, Problems: []string{}}
	if res.Lines == nil {
		res.Lines = []OrderLine{}
	}
	var regular int64
	for n, line := range lines {
		res.SubtotalCents += line.SubtotalCents
		if line.InMenu {
			regular += roundCents(float64(line.RegularPriceCents+deltas(line.Options)) * line.Quantity)
		} else {
			regular += line.SubtotalCents
		}
		for _, p := range line.Problems {
			res.Valid = false
			res.Problems = append(res.Problems, fmt.Sprintf("item %d (%s): %s", n+1, line.Name, p))
		}
	}
	res.TotalCents = res.SubtotalCents
	if regular > res.SubtotalCents {
		res.SavingsCents = regular - res.SubtotalCents
	}
	return res
}

// DraftRow is a priced cart as an order draft to store (status draft).
func DraftRow(r OrderResult, chat, source string, info OrderInfo) store.OrderDraftRow {
	b, _ := json.Marshal(r.Lines)
	return store.OrderDraftRow{Chat: chat, CustomerName: clipRunes(CleanLine(info.CustomerName), 200), Address: clipRunes(CleanLine(info.Address), 500),
		Notes: clipRunes(CleanText(info.Notes), MaxOrderNotes), Items: b, SubtotalCents: r.SubtotalCents, TotalCents: r.TotalCents,
		Currency: r.Currency, Valid: r.Valid, Source: source}
}

// FromDraft recounts a stored draft's lines.
func FromDraft(d store.OrderDraftRow) OrderResult {
	var lines []OrderLine
	_ = json.Unmarshal(d.Items, &lines)
	return Totals(lines, d.Currency)
}

func currencyOf(it store.MenuItem) string {
	if it.Currency == "" {
		return "BRL"
	}
	return it.Currency
}

func deltas(opts []OrderChoice) int64 {
	var d int64
	for _, o := range opts {
		d += o.PriceDeltaCents
	}
	return d
}

func roundCents(f float64) int64 { return int64(math.Round(f)) }

func priceLine(byID, byName map[string]store.MenuItem, li OrderLineInput, today string) OrderLine {
	line := OrderLine{ItemID: strings.TrimSpace(li.ItemID), Name: clipRunes(CleanLine(li.Name), MaxNameLen), Quantity: li.Quantity,
		Options: []OrderChoice{}, Notes: clipRunes(CleanText(li.Notes), MaxOrderNotes)}
	if li.Quantity <= 0 || li.Quantity > MaxOrderQuantity || math.IsNaN(li.Quantity) || math.IsInf(li.Quantity, 0) {
		line.Problems = append(line.Problems, fmt.Sprintf("quantidade deve ficar entre 1 e %d", MaxOrderQuantity))
		line.Quantity = 0
	}
	var it store.MenuItem
	var ok bool
	if line.ItemID != "" {
		it, ok = byID[line.ItemID]
	} else if line.Name != "" {
		it, ok = byName[FoldName(line.Name)]
	}
	if !ok {
		if line.Name == "" {
			line.Name = line.ItemID
		}
		if line.Name == "" {
			line.Problems = append(line.Problems, "informe item_id (de get_menu ou search_menu)")
		} else {
			line.Problems = append(line.Problems, "item não encontrado no cardápio")
		}
		if li.FallbackUnitCents > 0 && li.FallbackUnitCents <= MaxPriceCents {
			line.UnitPriceCents = li.FallbackUnitCents
			line.SubtotalCents = roundCents(float64(li.FallbackUnitCents) * line.Quantity)
		}
		line.ItemID = ""
		return line
	}
	line.ItemID, line.Name, line.InMenu, line.Available = it.ID, it.Name, true, it.Available
	line.RegularPriceCents, line.BasePriceCents, line.OnSale = it.PriceCents, EffectivePrice(it, today), OnSale(it, today)
	if !it.Available {
		line.Problems = append(line.Problems, "esgotado")
	}
	unit := line.BasePriceCents
	picked := map[int]int{}
	for _, raw := range li.Options {
		want := FoldName(raw)
		if want == "" || len(line.Options)+len(line.UnknownOptions) >= 30 {
			continue
		}
		found := false
		full := false
		for gi, o := range it.Options {
			for _, ch := range o.Choices {
				c := FoldName(ch.Name)
				if c != want && FoldName(o.Name+": "+ch.Name) != want && FoldName(o.Name+" "+ch.Name) != want {
					continue
				}
				if picked[gi] >= max(o.Max, 1) {
					full = true
					continue
				}
				picked[gi]++
				unit += ch.PriceDeltaCents
				line.Options = append(line.Options, OrderChoice{Group: o.Name, Choice: ch.Name, PriceDeltaCents: ch.PriceDeltaCents})
				found = true
				break
			}
			if found {
				break
			}
		}
		if !found {
			name := clipRunes(CleanLine(raw), 60)
			line.UnknownOptions = append(line.UnknownOptions, name)
			if full {
				line.Problems = append(line.Problems, fmt.Sprintf("opção %q passa do máximo de escolhas do grupo", name))
			} else {
				line.Problems = append(line.Problems, fmt.Sprintf("opção %q não existe neste item", name))
			}
		}
	}
	for gi, o := range it.Options {
		if o.Required && picked[gi] < max(o.Min, 1) {
			line.MissingOptions = append(line.MissingOptions, o.Name)
			line.Problems = append(line.Problems, fmt.Sprintf("escolha %s (obrigatório)", o.Name))
		}
	}
	if unit < 0 {
		unit = 0
	}
	line.UnitPriceCents = unit
	line.SubtotalCents = roundCents(float64(unit) * line.Quantity)
	return line
}

// OrderInfo is the rest of an order summary.
type OrderInfo struct {
	Title        string // default "Resumo do pedido"
	CustomerName string
	Address      string
	Notes        string
}

// FormatQuantity renders a quantity: "2", "1,5".
func FormatQuantity(q float64) string {
	if q == math.Trunc(q) {
		return strconv.FormatInt(int64(q), 10)
	}
	return strings.ReplaceAll(strconv.FormatFloat(q, 'f', -1, 64), ".", ",")
}

// OrderSummary renders a priced cart as a WhatsApp text: one line per item with its
// options and subtotal, the sale prices struck through, the total and the savings.
func OrderSummary(r OrderResult, info OrderInfo) string {
	title := CleanLine(info.Title)
	if title == "" {
		title = "Resumo do pedido"
	}
	var b strings.Builder
	b.WriteString("*" + title + "*\n")
	for _, l := range r.Lines {
		b.WriteString("\n" + FormatQuantity(l.Quantity) + "x " + l.Name + " — " + FormatPrice(l.SubtotalCents, r.Currency))
		if len(l.Options) > 0 {
			parts := make([]string, 0, len(l.Options))
			for _, o := range l.Options {
				p := o.Group + ": " + o.Choice
				if d := FormatDelta(o.PriceDeltaCents, r.Currency); d != "" {
					p += " (" + d + ")"
				}
				parts = append(parts, p)
			}
			b.WriteString("\n   " + strings.Join(parts, " · "))
		}
		if l.OnSale {
			b.WriteString("\n   promoção: ~" + FormatPrice(l.RegularPriceCents, r.Currency) + "~ *" + FormatPrice(l.BasePriceCents, r.Currency) + "* cada")
		}
		if l.Notes != "" {
			b.WriteString("\n   obs.: " + l.Notes)
		}
	}
	b.WriteString("\n\n*Total: " + FormatPrice(r.TotalCents, r.Currency) + "*")
	if r.SavingsCents > 0 {
		b.WriteString("\nVocê economiza " + FormatPrice(r.SavingsCents, r.Currency))
	}
	if n := CleanLine(info.CustomerName); n != "" {
		b.WriteString("\nCliente: " + clipRunes(n, 200))
	}
	if a := CleanLine(info.Address); a != "" {
		b.WriteString("\nEntrega: " + clipRunes(a, 500))
	}
	if n := CleanText(info.Notes); n != "" {
		b.WriteString("\nObs.: " + clipRunes(n, MaxOrderNotes))
	}
	return b.String()
}
