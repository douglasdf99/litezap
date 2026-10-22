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

// Caption is an item's WhatsApp text (the caption under its photo), with today's price.
func Caption(it store.MenuItem) string { return CaptionAt(it, Today()) }

// CaptionAt is Caption on day today: name, price (a sale struck through: "~R$ 50,00~
// *R$ 39,90*"), sold out, description, options and the item's link.
func CaptionAt(it store.MenuItem, today string) string {
	var b strings.Builder
	b.WriteString("*" + it.Name + "*")
	if p := PriceLabel(it, today); p != "" {
		b.WriteString(" — " + p)
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
	if it.LinkURL != "" {
		b.WriteString("\n" + it.LinkURL)
	}
	return b.String()
}

// ShortCaption is the one line under a photo of a collection: name and price.
func ShortCaption(it store.MenuItem, today string) string {
	s := "*" + it.Name + "*"
	if p := PriceLabel(it, today); p != "" {
		s += " — " + p
	}
	if !it.Available {
		s += " (esgotado)"
	}
	return s
}

// itemLines renders one item of the menu text.
func itemLines(it store.MenuItem, today string) string {
	var b strings.Builder
	b.WriteString("• " + it.Name)
	if p := PriceLabel(it, today); p != "" {
		b.WriteString(" — " + p)
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
	// Today is the day sale prices are checked against (default Today()).
	Today string
}

// listed reports whether an item belongs in a text for customers: never a hidden item,
// and a sold-out one only when asked.
func listed(it store.MenuItem, includeUnavailable bool) bool {
	return !it.Hidden && (it.Available || includeUnavailable)
}

type block struct {
	head  string
	intro string
	items []string
}

// Format renders the menu as WhatsApp text, grouped by category, split into chunks of
// at most ChunkRunes. Hidden items are never listed. It returns no chunks when nothing
// is listed.
func Format(cats []store.MenuCategory, items []store.MenuItem, opt FormatOptions) []string {
	title := opt.Title
	if title == "" {
		title = "Cardápio"
	}
	today := opt.Today
	if today == "" {
		today = Today()
	}
	var blocks []block
	byCat := map[string][]store.MenuItem{}
	for _, it := range items {
		if !listed(it, opt.IncludeUnavailable) {
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
			bl.items = append(bl.items, itemLines(it, today))
		}
		blocks = append(blocks, bl)
	}
	return chunk(title, blocks)
}

// CollectionOptions choose what a collection text shows.
type CollectionOptions struct {
	Title              string // default: the collection's name
	IncludeUnavailable bool
	Today              string
}

// FormatCollection renders a collection (its items in order, never hidden ones) as
// WhatsApp text, split like Format. items is the menu; the collection lists its ids.
func FormatCollection(c store.MenuCollection, items []store.MenuItem, opt CollectionOptions) []string {
	today := opt.Today
	if today == "" {
		today = Today()
	}
	title := CleanLine(opt.Title)
	if title == "" {
		title = c.Name
	}
	bl := block{intro: CleanText(c.Description)}
	for _, it := range CollectionItems(c, items) {
		if listed(it, opt.IncludeUnavailable) {
			bl.items = append(bl.items, itemLines(it, today))
		}
	}
	if len(bl.items) == 0 {
		return nil
	}
	return chunk(title, []block{bl})
}

// CollectionItems returns the collection's items, in its order.
func CollectionItems(c store.MenuCollection, items []store.MenuItem) []store.MenuItem {
	byID := map[string]store.MenuItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	out := make([]store.MenuItem, 0, len(c.ItemIDs))
	for _, id := range c.ItemIDs {
		if it, ok := byID[id]; ok {
			out = append(out, it)
		}
	}
	return out
}

// chunk joins a title and blocks into messages of at most ChunkRunes, splitting at
// item boundaries (a block split in two repeats its heading with "(cont.)").
func chunk(title string, blocks []block) []string {
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
		if bl.intro != "" {
			cur.WriteString("\n" + clipRunes(bl.intro, 500))
		}
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
