// Package menu holds the pure parts of the Cardápio (LiteZap's own catalog, kept in
// LiteZap and never written to Meta/WhatsApp): item validation, Brazilian price parsing,
// the CSV and "Montar com IA" importers, the WhatsApp text of an item or of the whole
// menu (split in chunks), and image normalization.
package menu

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/douglasdf99/litezap/internal/store"
)

// Field limits.
const (
	MaxNameLen        = 120
	MaxDescriptionLen = 1000
	MaxCategoryLen    = 60
	MaxSKULen         = 64
	MaxTags           = 10
	MaxTagLen         = 30
	MaxOptionNameLen  = 60
	MaxChoiceNameLen  = 60
	MaxImageURLLen    = 2048
	// MaxPriceCents is R$ 1.000.000,00; a choice delta is at most that, either sign.
	MaxPriceCents = 100_000_000
)

// Choice is one choice of an option group ("G", +R$ 10,00).
type Choice struct {
	Name            string `json:"name"`
	PriceDeltaCents int64  `json:"price_delta_cents"`
}

// Option is an option group of an item ("Tamanho": P, M, G).
type Option struct {
	ID       string   `json:"id,omitempty"`
	Name     string   `json:"name"`
	Min      int      `json:"min"`
	Max      int      `json:"max"`
	Required bool     `json:"required"`
	Choices  []Choice `json:"choices"`
}

// Row is an item to import (CSV, WhatsApp catalog or AI), as reviewed before saving.
type Row struct {
	Line        int    `json:"line,omitempty"`
	Category    string `json:"category"`
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceCents  int64  `json:"price_cents"`
	PriceText   string `json:"price_text,omitempty"`
	// Price is an input only: the price as written ("45,90", "R$ 1.234,50"), read into
	// price_cents by the rows import when given (never in an answer).
	Price     string   `json:"price,omitempty"`
	Available bool     `json:"available"`
	ImageURL  string   `json:"image_url,omitempty"`
	SKU       string   `json:"sku,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Options   []Option `json:"options"`
	Errors    []string `json:"errors,omitempty"`
	// Error joins Errors (one clear reason per invalid row); Problems are the same errors
	// with the field each one is about.
	Error    string    `json:"error,omitempty"`
	Problems []Problem `json:"problems,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
}

// Problem is one error of an imported row: its line, the field ("preco", "nome", ...;
// "" when it is about the whole row) and the reason, in Portuguese.
type Problem struct {
	Line    int    `json:"line"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

// AddError records an error of the row about field.
func (r *Row) AddError(field, msg string) {
	r.Errors = append(r.Errors, msg)
	r.Problems = append(r.Problems, Problem{Line: r.Line, Field: field, Message: msg})
}

// Check validates a normalized row, adding its problems, and fills Error.
func (r *Row) Check() {
	for _, p := range ValidateRowProblems(*r) {
		r.AddError(p.Field, p.Message)
	}
	r.Finish()
}

// Finish fills Error and gives every error a Problem (errors added without a field get
// field ""), with the row's line.
func (r *Row) Finish() {
	have := map[string]int{}
	for _, p := range r.Problems {
		have[p.Message]++
	}
	for _, e := range r.Errors {
		if have[e] > 0 {
			have[e]--
			continue
		}
		r.Problems = append(r.Problems, Problem{Field: "", Message: e})
	}
	for i := range r.Problems {
		r.Problems[i].Line = r.Line
	}
	r.Error = strings.Join(r.Errors, "; ")
}

// PriceError is the message of a price that could not be read.
func PriceError(field, text string) string {
	label := "preço"
	if field == "preco_promocional" {
		label = "preço promocional"
	}
	return fmt.Sprintf("%s inválido '%s' (use 45,90 ou R$ 1.234,50)", label, clipRunes(strings.TrimSpace(text), 40))
}

// invisible reports bidirectional controls and zero-width characters: they can make a
// name look different from what it is (a reversed price, a hidden word).
func invisible(r rune) bool {
	switch {
	case r >= 0x200B && r <= 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x2069,
		r == 0xFEFF, r == 0x061C, r == 0x180E:
		return true
	}
	return false
}

// CleanLine trims s and drops control, bidirectional and zero-width characters and
// repeated spaces (one line).
func CleanLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if invisible(r) {
			return -1
		}
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// CleanText trims s, keeps line breaks (at most two in a row) and drops other control
// characters.
func CleanText(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = CleanLine(l)
		if l == "" {
			blank++
			if blank > 1 || len(out) == 0 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func tooLong(s string, n int) bool { return utf8.RuneCountInString(s) > n }

// NormalizeOptions cleans option groups and makes min, max and required consistent:
// required means min >= 1; max defaults to 1 and never exceeds the number of choices.
func NormalizeOptions(opts []Option) []Option {
	out := make([]Option, 0, len(opts))
	for _, o := range opts {
		o.Name = CleanLine(o.Name)
		choices := make([]Choice, 0, len(o.Choices))
		for _, c := range o.Choices {
			c.Name = CleanLine(c.Name)
			choices = append(choices, c)
		}
		o.Choices = choices
		if o.Required && o.Min < 1 {
			o.Min = 1
		}
		if o.Min > 0 {
			o.Required = true
		}
		if o.Max <= 0 {
			o.Max = max(1, o.Min)
		}
		if len(o.Choices) > 0 && o.Max > len(o.Choices) {
			o.Max = len(o.Choices)
		}
		out = append(out, o)
	}
	return out
}

// ValidateOptions checks normalized option groups.
func ValidateOptions(opts []Option) error {
	if len(opts) > store.MaxMenuOptions {
		return fmt.Errorf("at most %d option groups per item", store.MaxMenuOptions)
	}
	for _, o := range opts {
		switch {
		case o.Name == "" || tooLong(o.Name, MaxOptionNameLen):
			return fmt.Errorf("an option group needs a name (at most %d characters)", MaxOptionNameLen)
		case len(o.Choices) == 0 || len(o.Choices) > store.MaxMenuChoices:
			return fmt.Errorf("option %q needs 1 to %d choices", o.Name, store.MaxMenuChoices)
		case o.Min < 0 || o.Min > o.Max || o.Max > len(o.Choices):
			return fmt.Errorf("option %q: min and max must satisfy 0 <= min <= max <= number of choices", o.Name)
		}
		seen := map[string]bool{}
		for _, c := range o.Choices {
			key := strings.ToLower(c.Name)
			switch {
			case c.Name == "" || tooLong(c.Name, MaxChoiceNameLen):
				return fmt.Errorf("option %q: every choice needs a name (at most %d characters)", o.Name, MaxChoiceNameLen)
			case seen[key]:
				return fmt.Errorf("option %q: choice %q is repeated", o.Name, c.Name)
			case c.PriceDeltaCents > MaxPriceCents || c.PriceDeltaCents < -MaxPriceCents:
				return fmt.Errorf("option %q: price deltas are limited to R$ 1.000.000,00", o.Name)
			}
			seen[key] = true
		}
	}
	return nil
}

// NormalizeRow cleans a row in place (names on one line, tags lowercased, options
// normalized).
func NormalizeRow(r *Row) {
	r.Category, r.Name, r.SKU = CleanLine(r.Category), CleanLine(r.Name), CleanLine(r.SKU)
	r.Description = CleanText(r.Description)
	r.ImageURL = strings.TrimSpace(r.ImageURL)
	tags := []string{}
	for _, t := range r.Tags {
		if t = strings.ToLower(CleanLine(t)); t != "" && !contains(tags, t) {
			tags = append(tags, t)
		}
	}
	r.Tags = tags
	r.Options = NormalizeOptions(r.Options)
	if r.Options == nil {
		r.Options = []Option{}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ValidateRow returns the problems of a normalized row (nil when it can be saved).
func ValidateRow(r Row) []string {
	var out []string
	for _, p := range ValidateRowProblems(r) {
		out = append(out, p.Message)
	}
	return out
}

// ValidateRowProblems returns the problems of a normalized row with their fields.
func ValidateRowProblems(r Row) []Problem {
	var ps []Problem
	add := func(field, msg string) { ps = append(ps, Problem{Line: r.Line, Field: field, Message: msg}) }
	if r.Name == "" || tooLong(r.Name, MaxNameLen) {
		add("nome", fmt.Sprintf("nome é obrigatório (até %d caracteres)", MaxNameLen))
	}
	if tooLong(r.Description, MaxDescriptionLen) {
		add("descricao", fmt.Sprintf("descrição tem no máximo %d caracteres", MaxDescriptionLen))
	}
	if tooLong(r.Category, MaxCategoryLen) {
		add("categoria", fmt.Sprintf("categoria tem no máximo %d caracteres", MaxCategoryLen))
	}
	if r.PriceCents < 0 || r.PriceCents > MaxPriceCents {
		add("preco", "preço deve ficar entre R$ 0,00 e R$ 1.000.000,00")
	}
	if tooLong(r.SKU, MaxSKULen) {
		add("sku", fmt.Sprintf("sku tem no máximo %d caracteres", MaxSKULen))
	}
	if len(r.Tags) > MaxTags {
		add("tags", fmt.Sprintf("no máximo %d tags", MaxTags))
	}
	for _, t := range r.Tags {
		if tooLong(t, MaxTagLen) {
			add("tags", fmt.Sprintf("cada tag tem no máximo %d caracteres", MaxTagLen))
			break
		}
	}
	if r.ImageURL != "" && !validImageURL(r.ImageURL) {
		add("imagem_url", "imagem_url deve ser um endereço http(s) completo")
	}
	if err := ValidateOptions(r.Options); err != nil {
		add("opcoes", err.Error())
	}
	return ps
}

func validImageURL(s string) bool {
	if len(s) > MaxImageURLLen {
		return false
	}
	l := strings.ToLower(s)
	return (strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://")) && len(s) > len("https://") && !strings.ContainsAny(s, " \t\n")
}

// ErrPrice is a price that could not be read.
var ErrPrice = errors.New("preço inválido")

// ParsePrice reads a price in reais as Brazilians write it and returns cents: "45",
// "45,9", "45,90", "R$ 1.234,50", "1234.50", "1.234" (thousands), "R$45". An empty
// price is 0. When both separators appear, the last one is the decimal separator; a
// lone separator followed by exactly three digits groups thousands.
func ParsePrice(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(s, "R$"), "r$"))
	s = strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "\u00a0", "")
	if s == "" {
		return 0, nil
	}
	if strings.HasPrefix(s, "-") {
		return 0, ErrPrice
	}
	s = strings.TrimPrefix(s, "+")
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' && r != ',' {
			return 0, ErrPrice
		}
	}
	lastDot, lastComma := strings.LastIndex(s, "."), strings.LastIndex(s, ",")
	var intPart, frac string
	switch {
	case lastDot >= 0 && lastComma >= 0:
		dec := max(lastDot, lastComma)
		thousands := "."
		if dec == lastDot {
			thousands = ","
		}
		intPart, frac = s[:dec], s[dec+1:]
		if strings.ContainsAny(frac, ".,") || strings.Contains(intPart, string(s[dec])) {
			return 0, ErrPrice
		}
		if !groupedThousands(intPart, thousands) {
			return 0, ErrPrice
		}
		intPart = strings.ReplaceAll(intPart, thousands, "")
	case lastDot >= 0 || lastComma >= 0:
		sep := "."
		if lastComma >= 0 {
			sep = ","
		}
		parts := strings.Split(s, sep)
		last := parts[len(parts)-1]
		if len(parts) > 2 || len(last) == 3 { // thousands: "1.234", "1.234.567"
			if !groupedThousands(s, sep) {
				return 0, ErrPrice
			}
			intPart = strings.ReplaceAll(s, sep, "")
		} else {
			intPart, frac = parts[0], last
		}
	default:
		intPart = s
	}
	if intPart == "" {
		intPart = "0"
	}
	if len(frac) > 2 || len(intPart) > 9 {
		return 0, ErrPrice
	}
	n, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, ErrPrice
	}
	cents := n * 100
	switch len(frac) {
	case 1:
		cents += int64(frac[0]-'0') * 10
	case 2:
		f, _ := strconv.Atoi(frac)
		cents += int64(f)
	}
	if cents > MaxPriceCents {
		return 0, ErrPrice
	}
	return cents, nil
}

// groupedThousands checks "1.234.567": a first group of 1-3 digits, then groups of 3.
func groupedThousands(s, sep string) bool {
	parts := strings.Split(s, sep)
	if len(parts) == 1 {
		return true
	}
	if len(parts[0]) == 0 || len(parts[0]) > 3 {
		return false
	}
	for _, p := range parts[1:] {
		if len(p) != 3 {
			return false
		}
	}
	return true
}

// PriceFromFloat converts a price in units (an AI's 45.9) to cents.
func PriceFromFloat(f float64) (int64, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f*100 > MaxPriceCents {
		return 0, ErrPrice
	}
	return int64(math.Round(f * 100)), nil
}

// FormatPrice renders cents: "R$ 1.234,50" for BRL, "USD 12,50" otherwise.
func FormatPrice(cents int64, currency string) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	reais, c := cents/100, cents%100
	s := strconv.FormatInt(reais, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	sym := "R$"
	if currency != "" && currency != "BRL" {
		sym = currency
	}
	out := fmt.Sprintf("%s %s,%02d", sym, b.String(), c)
	if neg {
		out = "-" + out
	}
	return out
}

// FormatDelta renders a choice's price delta: "+R$ 5,00", "-R$ 2,00", "" for zero.
func FormatDelta(cents int64, currency string) string {
	switch {
	case cents > 0:
		return "+" + FormatPrice(cents, currency)
	case cents < 0:
		return FormatPrice(cents, currency)
	}
	return ""
}

// RowToItem converts a validated row into a store item (no category, image or id).
func RowToItem(r Row) store.MenuItem {
	return store.MenuItem{Name: r.Name, Description: r.Description, PriceCents: r.PriceCents, Currency: "BRL",
		Available: r.Available, SKU: r.SKU, Tags: r.Tags, Options: ToStoreOptions(r.Options)}
}

// ToStoreOptions converts option groups to their stored form.
func ToStoreOptions(opts []Option) []store.MenuOption {
	out := make([]store.MenuOption, 0, len(opts))
	for _, o := range opts {
		so := store.MenuOption{ID: o.ID, Name: o.Name, Min: o.Min, Max: o.Max, Required: o.Required, Choices: []store.MenuChoice{}}
		for _, c := range o.Choices {
			so.Choices = append(so.Choices, store.MenuChoice{Name: c.Name, PriceDeltaCents: c.PriceDeltaCents})
		}
		out = append(out, so)
	}
	return out
}

// FromStoreOptions converts stored option groups.
func FromStoreOptions(opts []store.MenuOption) []Option {
	out := make([]Option, 0, len(opts))
	for _, o := range opts {
		v := Option{ID: o.ID, Name: o.Name, Min: o.Min, Max: o.Max, Required: o.Required, Choices: []Choice{}}
		for _, c := range o.Choices {
			v.Choices = append(v.Choices, Choice{Name: c.Name, PriceDeltaCents: c.PriceDeltaCents})
		}
		out = append(out, v)
	}
	return out
}
