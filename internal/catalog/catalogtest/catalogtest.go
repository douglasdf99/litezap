// Package catalogtest is a fake of the public catalog GraphQL endpoint and of the media
// CDN, answering with the recorded shapes of the research (no network).
package catalogtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/douglasdf99/litezap/internal/catalog"
)

// Product is a fake catalog product.
type Product struct {
	ID, Name, Description, Currency string
	Price                           int64 // thousandths
	Hidden                          bool
	Status                          string // APPROVED (default), PENDING...
	NoImage                         bool
}

// Fake answers catalog reads by business JID.
type Fake struct {
	mu          sync.Mutex
	Catalogs    map[string][]Product // jid -> products (absent: no catalog)
	Collections map[string]map[string][]string
	Orders      map[string]string // order id -> raw order JSON (the "order" object)
	// Fail, when set, is the raw answer (and status) of every GraphQL call.
	FailStatus int
	FailBody   string
	Requests   []map[string]any
	Images     int // image downloads
	Proxies    []string
	// Hold, when set, makes every request wait until it is closed (concurrency tests).
	Hold chan struct{}
}

// New returns an empty fake.
func New() *Fake {
	return &Fake{Catalogs: map[string][]Product{}, Collections: map[string]map[string][]string{}, Orders: map[string]string{}}
}

// JPEG is a small valid JPEG (the image every product has).
func JPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		img.Set(x, x, color.RGBA{200, 10, 10, 255})
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

func (f *Fake) wire(p Product) map[string]any {
	st := p.Status
	if st == "" {
		st = "APPROVED"
	}
	hidden := "ISHIDDEN_FALSE"
	if p.Hidden {
		hidden = "ISHIDDEN_TRUE"
	}
	images := []any{}
	if !p.NoImage {
		images = append(images, map[string]any{"id": "1", "original_image_url": "https://mmg.whatsapp.net/v/t45/" + p.ID + ".jpg?oh=x",
			"request_image_url": "https://mmg.whatsapp.net/r/" + p.ID + ".jpg"})
	}
	return map[string]any{"id": p.ID, "name": p.Name, "description": p.Description, "price": fmt.Sprint(p.Price),
		"currency": p.Currency, "is_hidden": hidden, "availability": "IN_STOCK", "status_info": map[string]any{"status": st},
		"media": map[string]any{"images": images, "videos": []any{}}}
}

// Doer routes requests to the fake (GraphQL and CDN images), as a proxied client would.
func (f *Fake) Doer(proxy string) catalog.Doer {
	f.mu.Lock()
	f.Proxies = append(f.Proxies, proxy)
	f.mu.Unlock()
	return doer(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		f.serve(rec, r)
		return rec.Result(), nil
	})
}

type doer func(*http.Request) (*http.Response, error)

func (d doer) Do(r *http.Request) (*http.Response, error) { return d(r) }

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	hold := f.Hold
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasSuffix(r.URL.Host, ".whatsapp.net") {
		f.Images++
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(JPEG())
		return
	}
	b, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(b, &req)
	f.Requests = append(f.Requests, req)
	if f.FailBody != "" {
		if f.FailStatus != 0 {
			w.WriteHeader(f.FailStatus)
		}
		_, _ = io.WriteString(w, f.FailBody)
		return
	}
	vars, _ := req["variables"].(map[string]any)
	rq, _ := vars["request"].(map[string]any)
	answer := func(root string, v any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{root: v}})
	}
	switch {
	case rq["product_catalog"] != nil:
		pc := rq["product_catalog"].(map[string]any)
		list, ok := f.Catalogs[fmt.Sprint(pc["jid"])]
		if !ok {
			answer("xwa_product_catalog_get_product_catalog", nil)
			return
		}
		ps := []any{}
		for _, p := range list {
			ps = append(ps, f.wire(p))
		}
		answer("xwa_product_catalog_get_product_catalog", map[string]any{"product_catalog": map[string]any{
			"products": ps, "paging": map[string]any{"before": nil, "after": nil}}})
	case rq["product"] != nil:
		pr := rq["product"].(map[string]any)
		for _, p := range f.Catalogs[fmt.Sprint(pr["jid"])] {
			if p.ID == pr["product_id"] {
				answer("xwa_product_catalog_get_product", map[string]any{"product_catalog": map[string]any{"product": f.wire(p)}})
				return
			}
		}
		answer("xwa_product_catalog_get_product", map[string]any{"product_catalog": map[string]any{"product": nil}})
	case rq["collections"] != nil:
		c := rq["collections"].(map[string]any)
		jid := fmt.Sprint(c["biz_jid"])
		cols := []any{}
		for name, ids := range f.Collections[jid] {
			ps := []any{}
			for _, p := range f.Catalogs[jid] {
				for _, id := range ids {
					if p.ID == id {
						ps = append(ps, f.wire(p))
					}
				}
			}
			cols = append(cols, map[string]any{"id": "col_" + name, "name": name, "products": ps,
				"status_info": map[string]any{"status": "APPROVED"}})
		}
		answer("xwa_product_catalog_get_collections", map[string]any{"collections": cols, "paging": map[string]any{}})
	case rq["order"] != nil:
		o := rq["order"].(map[string]any)
		raw, ok := f.Orders[fmt.Sprint(o["id"])]
		if !ok {
			_, _ = io.WriteString(w, `{"data":{"xwa_checkout_get_order_info":null}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"xwa_checkout_get_order_info":{"order":`+raw+`}}}`)
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":100,"message":"unknown query"}}`)
	}
}

// ImageCount returns how many images the fake served.
func (f *Fake) ImageCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Images
}

// Calls returns how many GraphQL requests the fake answered.
func (f *Fake) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Requests)
}

// Client returns a catalog client for this fake (pair it with Doer as the HTTP hook).
func (f *Fake) Client() *catalog.Client {
	c := catalog.Defaults()
	c.Endpoint = "https://graph.whatsapp.test/graphql/catalog"
	return catalog.New(c)
}
