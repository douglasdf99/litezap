package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Recorded shapes (research §2.1-§2.3, §6): the public endpoint answers persisted queries
// with data.<root>; numbers may be strings.
const catalogPage = `{"data":{"xwa_product_catalog_get_product_catalog":{"product_catalog":{
 "products":[
  {"id":"8364718273645","name":"Camiseta","description":"Algodão","price":"59900","currency":"BRL","retailer_id":"SKU-1",
   "url":"https://loja.x/c","is_hidden":"ISHIDDEN_FALSE","availability":"IN_STOCK","max_available":null,
   "sale_price":{"price":"49900","start_date":null,"end_date":null},"status_info":{"status":"APPROVED","can_appeal":false},
   "media":{"images":[{"id":"1","original_image_url":"https://mmg.whatsapp.net/v/t45/a.jpg?x=1","request_image_url":"https://mmg.whatsapp.net/r.jpg"}],"videos":[]}},
  {"id":8364718273646,"name":"Bolo","price":25000,"currency":"BRL","is_hidden":"ISHIDDEN_TRUE","status_info":{"status":"PENDING"},"media":{"images":[]}}
 ],
 "paging":{"before":null,"after":"CURSOR2"}}}}}`

const productAnswer = `{"data":{"xwa_product_catalog_get_product":{"product_catalog":{"product":
 {"id":"8364718273645","name":"Camiseta","price":"59900","currency":"BRL","media":{"images":[{"original_image_url":"https://mmg.whatsapp.net/a.jpg"}]}}}}}}`

const collectionsAnswer = `{"data":{"xwa_product_catalog_get_collections":{"collections":[
 {"id":"c1","name":"Verão","status_info":{"status":"APPROVED","reject_reason":null,"commerce_url":null},
  "products":[{"id":"8364718273645","name":"Camiseta","price":"59900","currency":"BRL"}]}],"paging":{"after":null}}}}`

const orderAnswer = `{"data":{"xwa_checkout_get_order_info":{"order":{"id":"1234567890","creation_ts":"1800000000",
 "price":{"subtotal":"100000","total":"119800","currency":"BRL"},
 "products":[{"id":"77","name":"Bolo","price":"25000","currency":"BRL","quantity":"2","image":{"url":"https://mmg.whatsapp.net/b.jpg"}}]}}}}`

type fake struct {
	status int
	answer string
	seen   []map[string]any
}

func (f *fake) server(t *testing.T) (*Client, *http.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request = %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		f.seen = append(f.seen, m)
		if f.status != 0 {
			w.WriteHeader(f.status)
		}
		_, _ = io.WriteString(w, f.answer)
	}))
	t.Cleanup(srv.Close)
	cfg := Defaults()
	cfg.Endpoint = srv.URL
	return New(cfg), srv.Client()
}

func TestCatalogPage(t *testing.T) {
	f := &fake{answer: catalogPage}
	c, d := f.server(t)
	page, err := c.Catalog(context.Background(), d, "5511999990000@s.whatsapp.net", 50, "CURSOR1")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Products) != 2 || page.After != "CURSOR2" {
		t.Fatalf("page = %+v", page)
	}
	p := page.Products[0]
	if p.ID != "8364718273645" || p.PriceAmount1000 != 59900 || p.SaleAmount1000 != 49900 || p.Hidden || p.Availability != "in_stock" ||
		p.ReviewStatus != "approved" || len(p.Images) != 1 || !strings.HasPrefix(p.Images[0], "https://mmg.whatsapp.net/v/t45/") {
		t.Fatalf("product = %+v", p)
	}
	if q := page.Products[1]; q.ID != "8364718273646" || q.PriceAmount1000 != 25000 || !q.Hidden || q.ReviewStatus != "pending" || q.Images == nil {
		t.Fatalf("product 2 = %+v", q)
	}
	req := f.seen[0]
	vars := req["variables"].(map[string]any)["request"].(map[string]any)["product_catalog"].(map[string]any)
	if req["doc_id"] != "30445081048424116" || req["access_token"] != DefaultAccessToken || req["lang"] != "pt_BR" ||
		vars["jid"] != "5511999990000@s.whatsapp.net" || vars["limit"] != "50" || vars["after"] != "CURSOR1" ||
		vars["allow_shop_source"] != "ALLOWSHOPSOURCE_TRUE" || vars["width"] != "100" {
		t.Fatalf("request = %v", req)
	}
}

func TestProductCollectionsOrder(t *testing.T) {
	ctx := context.Background()
	f := &fake{answer: productAnswer}
	c, d := f.server(t)
	p, err := c.Product(ctx, d, "5561@s.whatsapp.net", "8364718273645")
	if err != nil || p.Name != "Camiseta" || len(p.Images) != 1 {
		t.Fatalf("product = %+v, %v", p, err)
	}
	if v := f.seen[0]["variables"].(map[string]any)["request"].(map[string]any)["product"].(map[string]any); v["product_id"] != "8364718273645" {
		t.Fatalf("product request = %v", v)
	}
	f.answer = collectionsAnswer
	cols, err := c.Collections(ctx, d, "5561@s.whatsapp.net")
	if err != nil || len(cols) != 1 || cols[0].Name != "Verão" || cols[0].ReviewStatus != "approved" || len(cols[0].Products) != 1 {
		t.Fatalf("collections = %+v, %v", cols, err)
	}
	f.answer = orderAnswer
	o, err := c.Order(ctx, d, "5561@s.whatsapp.net", "1234567890", "dG9r")
	if err != nil || o.TotalAmount1000 != 119800 || o.CreatedAt.Unix() != 1_800_000_000 || len(o.Items) != 1 || o.Items[0].Quantity != 2 {
		t.Fatalf("order = %+v, %v", o, err)
	}
	tok := f.seen[2]["variables"].(map[string]any)["request"].(map[string]any)["order"].(map[string]any)["token"].(map[string]any)
	if tok["sensitive_string_value"] != "dG9r" || f.seen[2]["doc_id"] != "26593811266898374" {
		t.Fatalf("order request = %v", f.seen[2])
	}
}

func TestErrorsMap(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		status int
		answer string
		want   error
	}{
		"no catalog root null": {answer: `{"data":{"xwa_product_catalog_get_product_catalog":null}}`, want: ErrNotFound},
		"no catalog code":      {answer: `{"data":null,"errors":[{"code":2498052,"message":"no catalog"}]}`, want: ErrNotFound},
		"rate limited code":    {answer: `{"errors":[{"code":1675004,"message":"RATE_LIMIT_EXCEEDED"}]}`, want: ErrRateLimited},
		"rate limited http":    {status: 429, answer: `{}`, want: ErrRateLimited},
		"token changed":        {status: 400, answer: `{"error":{"code":1675002,"message":"unauthorized"}}`, want: ErrUpstreamChanged},
		"doc id rotated":       {status: 400, answer: `{"errors":[{"code":100,"message":"persisted query not found"}]}`, want: ErrUpstreamChanged},
		"root renamed":         {answer: `{"data":{"xwa_product_catalog_get_product_catalog_v2":{}}}`, want: ErrUpstreamChanged},
		"not json":             {answer: `<html>`, want: ErrUpstreamChanged},
		"server down":          {status: 503, answer: `oops`, want: ErrUnavailable},
		"commerce disabled":    {answer: `{"errors":[{"code":2498048}]}`, want: ErrCommerceDisabled},
	} {
		f := &fake{status: tc.status, answer: tc.answer}
		c, d := f.server(t)
		_, err := c.Catalog(ctx, d, "1@s.whatsapp.net", 10, "")
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	// An empty catalog is a page, not an error.
	f := &fake{answer: `{"data":{"xwa_product_catalog_get_product_catalog":{"product_catalog":{"products":[],"paging":{"after":"X"}}}}}`}
	c, d := f.server(t)
	if p, err := c.Catalog(ctx, d, "1@s.whatsapp.net", 10, ""); err != nil || len(p.Products) != 0 || p.After != "" {
		t.Fatalf("empty = %+v, %v", p, err)
	}
	// A doc_id from config is used as is.
	c.Config.DocIDs.Catalog = "111"
	_, _ = c.Catalog(ctx, d, "1@s.whatsapp.net", 10, "")
	if f.seen[1]["doc_id"] != "111" {
		t.Fatalf("doc_id = %v", f.seen[1]["doc_id"])
	}
}

func TestResponseCap(t *testing.T) {
	f := &fake{answer: `{"data":{"x":"` + strings.Repeat("a", MaxResponse) + `"}}`}
	c, d := f.server(t)
	if _, err := c.Catalog(context.Background(), d, "1@s.whatsapp.net", 10, ""); !errors.Is(err, ErrUpstreamChanged) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("cap = %v", err)
	}
}

func TestImageHostsAndHelpers(t *testing.T) {
	for u, ok := range map[string]bool{
		"https://mmg.whatsapp.net/v/t45/a.jpg": true, "https://scontent.xx.fbcdn.net/a.jpg": true,
		"http://mmg.whatsapp.net/a.jpg": false, "https://evil.example/a.jpg": false, "https://whatsapp.net.evil.x/a": false,
		"https://user@mmg.whatsapp.net/a": false, "https://mmg.whatsapp.net:8443/a": false, "https://127.0.0.1/a": false,
	} {
		if ImageHostAllowed(u) != ok {
			t.Errorf("%s: %v", u, !ok)
		}
	}
	if Link("5511999990000") != "https://wa.me/c/5511999990000" || Link("55@x") != "" {
		t.Fatal("link")
	}
	for in, want := range map[int64]string{12500: "R$ 12,50", 0: "R$ 0,00", 1234567890: "R$ 1.234.567,89", 59990: "R$ 59,99", 5: "R$ 0,01"} {
		if got := FormatBRL(in); got != want {
			t.Errorf("FormatBRL(%d) = %q, want %q", in, got, want)
		}
	}
	if err := (Config{DocIDs: DocIDs{Catalog: "12a"}}).Validate(); err == nil {
		t.Fatal("a non-numeric doc_id must be refused")
	}
	if _, err := NewHTTPClient("socks5://127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
}
