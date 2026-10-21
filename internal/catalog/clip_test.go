package catalog

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestProductTextClippedAtParse(t *testing.T) {
	long := strings.Repeat("é", 300)
	f := &fake{answer: `{"data":{"xwa_product_catalog_get_product":{"product_catalog":{"product":{"id":"1","name":"` + long +
		`","description":"` + strings.Repeat("d", 6000) + `","url":"https://x.y/` + strings.Repeat("u", 2000) + `","price":"1"}}}}}`}
	c, d := f.server(t)
	p, err := c.Product(context.Background(), d, "1@s.whatsapp.net", "1")
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(p.Name) != maxName || len(p.Description) != maxDescription || p.URL != "" {
		t.Fatalf("name %d, description %d, url %q", utf8.RuneCountInString(p.Name), len(p.Description), p.URL)
	}
	if ImageHostAllowed("https://x.fbsbx.com/a.jpg") {
		t.Fatal("fbsbx.com is not a product image host")
	}
}
