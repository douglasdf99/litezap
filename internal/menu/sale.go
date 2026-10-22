package menu

import (
	"errors"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/store"
)

// Sale prices: an item may have a sale price lower than its price, optionally until a
// date (its last day). The effective price is the sale price while the sale lasts; the
// order engine, the attendant and every text use it.

// DateLayout is how sale dates are stored.
const DateLayout = "2006-01-02"

// ErrDate is a date that could not be read.
var ErrDate = errors.New("data inválida")

// Today is the date sales are checked against (local time; a variable for tests).
var Today = func() string { return time.Now().Format(DateLayout) }

// ParseDate reads "2026-12-31" or "31/12/2026" and returns YYYY-MM-DD ("" stays "").
func ParseDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	for _, layout := range []string{DateLayout, "02/01/2006", "2/1/2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format(DateLayout), nil
		}
	}
	return "", ErrDate
}

// OnSale reports whether the item's sale price applies on day today (YYYY-MM-DD).
func OnSale(it store.MenuItem, today string) bool {
	return it.SalePriceCents > 0 && it.SalePriceCents < it.PriceCents && (it.SaleUntil == "" || today <= it.SaleUntil)
}

// EffectivePrice is the price an item is sold for on day today: its sale price while the
// sale lasts, its price otherwise.
func EffectivePrice(it store.MenuItem, today string) int64 {
	if OnSale(it, today) {
		return it.SalePriceCents
	}
	return it.PriceCents
}

// PriceLabel renders an item's price for WhatsApp: "R$ 45,90", or during a sale the old
// price struck through and the sale price in bold, "~R$ 50,00~ *R$ 39,90*". It is "" for
// an item without a price.
func PriceLabel(it store.MenuItem, today string) string {
	if OnSale(it, today) {
		return "~" + FormatPrice(it.PriceCents, it.Currency) + "~ *" + FormatPrice(it.SalePriceCents, it.Currency) + "*"
	}
	if it.PriceCents > 0 {
		return FormatPrice(it.PriceCents, it.Currency)
	}
	return ""
}
