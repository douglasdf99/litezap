package menu

import (
	"strings"
	"unicode/utf8"

	"github.com/douglasdf99/litezap/internal/store"
)

// ChunkRunes is the size of one menu message: long menus are split at category and
// item boundaries so each WhatsApp message stays readable.
const ChunkRunes = 3500

// MaxChunks bounds the messages of one menu send.
const MaxChunks = 10

// OptionsLine renders an option group: "Tamanho: P · M (+R$ 5,00) · G (+R$ 10,00)".
func OptionsLine(o store.MenuOption, currency string) string {
	parts := make([]string, 0, len(o.Choices))
	for _, c := range o.Choices {
		p := c.Name
		if d := FormatDelta(c.PriceDeltaCents, currency); d != "" {
			p += " (" + d + ")"
		}
		parts = append(parts, p)
	}
	label := o.Name
	switch {
	case !o.Required && o.Max > 1:
		label += " (opcional, até " + itoa(o.Max) + ")"
	case !o.Required:
		label += " (opcional)"
	case o.Max > 1:
		label += " (escolha até " + itoa(o.Max) + ")"
	}
	return label + ": " + strings.Join(parts, " · ")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Caption is an item's WhatsApp text (the caption under its photo).
func Caption(it store.MenuItem) string {
	var b strings.Builder
	b.WriteString("*" + it.Name + "*")
	if it.PriceCents > 0 {
		b.WriteString(" — " + FormatPrice(it.PriceCents, it.Currency))
	}
	if !it.Available {
		b.WriteString(" (esgotado)")
	}
	if it.Description != "" {
		b.WriteString("\n" + it.Description)
	}
	for _, o := range it.Options {
		b.WriteString("\n" + OptionsLine(o, it.Currency))
	}
	return b.String()
}

// itemLines renders one item of the menu text.
func itemLines(it store.MenuItem) string {
	var b strings.Builder
	b.WriteString("• " + it.Name)
	if it.PriceCents > 0 {
		b.WriteString(" — " + FormatPrice(it.PriceCents, it.Currency))
	}
	if !it.Available {
		b.WriteString(" (esgotado)")
	}
	if d := CleanLine(it.Description); d != "" {
		b.WriteString("\n  " + clipRunes(d, 160))
	}
	for _, o := range it.Options {
		b.WriteString("\n  " + OptionsLine(o, it.Currency))
	}
	return b.String()
}

// FormatOptions choose what a menu text shows.
type FormatOptions struct {
	Title string // first line (default "Cardápio")
	// CategoryID limits the text to one category ("" = all).
	CategoryID string
	// IncludeUnavailable also lists the items marked esgotado.
	IncludeUnavailable bool
}

// Format renders the menu as WhatsApp text, grouped by category, split into chunks of
// at most ChunkRunes. It returns no chunks when nothing is listed.
func Format(cats []store.MenuCategory, items []store.MenuItem, opt FormatOptions) []string {
	title := opt.Title
	if title == "" {
		title = "Cardápio"
	}
	type block struct {
		head  string
		items []string
	}
	var blocks []block
	byCat := map[string][]store.MenuItem{}
	for _, it := range items {
		if !it.Available && !opt.IncludeUnavailable {
			continue
		}
		byCat[it.CategoryID] = append(byCat[it.CategoryID], it)
	}
	order := make([]store.MenuCategory, 0, len(cats)+1)
	order = append(order, cats...)
	order = append(order, store.MenuCategory{ID: "", Name: "Outros"})
	for _, c := range order {
		if opt.CategoryID != "" && c.ID != opt.CategoryID {
			continue
		}
		list := byCat[c.ID]
		if len(list) == 0 {
			continue
		}
		bl := block{head: "*" + c.Name + "*"}
		if c.ID == "" && len(byCat) == 1 {
			bl.head = "" // a menu without categories needs no "Outros" heading
		}
		for _, it := range list {
			bl.items = append(bl.items, itemLines(it))
		}
		blocks = append(blocks, bl)
	}
	if len(blocks) == 0 {
		return nil
	}
	var chunks []string
	var cur strings.Builder
	size := func() int { return utf8.RuneCountInString(cur.String()) }
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
	}
	cur.WriteString("*" + title + "*")
	for _, bl := range blocks {
		head := bl.head
		for i, line := range bl.items {
			add := ""
			if i == 0 {
				if head != "" {
					add = "\n\n" + head + "\n" + line
				} else {
					add = "\n\n" + line
				}
			} else {
				add = "\n" + line
			}
			if size()+utf8.RuneCountInString(add) > ChunkRunes && size() > 0 {
				flush()
				if i > 0 && head != "" {
					add = strings.TrimSuffix(head, "*") + " (cont.)*\n" + line
				} else {
					add = strings.TrimLeft(add, "\n")
				}
			}
			if utf8.RuneCountInString(add) > ChunkRunes { // one huge item: cut it
				add = clipRunes(add, ChunkRunes)
			}
			cur.WriteString(add)
		}
	}
	flush()
	return chunks
}
