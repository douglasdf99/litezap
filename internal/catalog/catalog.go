// Package catalog reads WhatsApp Business catalogs (products, collections) and order
// details from the public catalog GraphQL endpoint WA Web uses (marco 12, catalog phase 1;
// research in docs/superpowers/specs/2026-10-21-catalog-protocol-research.md §2, §6).
//
// The endpoint, the app token and the persisted query ids (doc_ids) are WA Web internals:
// Meta can rotate them at any release. They are configuration (Config), never constants,
// and a change shows up as ErrUpstreamChanged with a message saying what to update.
// Reads are not tied to the WhatsApp account (no session), so the ban risk to the number
// is low, but the endpoint rate-limits by IP: callers cache results and send each
// instance's reads through that instance's proxy.
package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultEndpoint is the public catalog GraphQL endpoint (WAWebRelayEnvironment).
const DefaultEndpoint = "https://graph.whatsapp.com/graphql/catalog"

// DefaultAccessToken is WHATSAPP_GRAPHQL_CATALOG_ACCESS_TOKEN, the public app token in
// the WA Web bundle (2026-10-21 snapshot).
const DefaultAccessToken = "WA|787118555984857|7bb1544a3599aa180ac9a3f7688ba243"

// DocIDs are the persisted query ids of the read operations (WAWebGraphQLPersistedQueries).
type DocIDs struct {
	Catalog     string `yaml:"catalog" json:"catalog"`         // WAWebQueryCatalogQuery
	Product     string `yaml:"product" json:"product"`         // WAWebQueryCatalogProductQuery
	Collections string `yaml:"collections" json:"collections"` // WAWebQueryProductCollectionsQuery
	Order       string `yaml:"order" json:"order"`             // WAWebBizQueryOrderJobQuery
}

// Config is the catalog transport. Zero fields take the defaults.
type Config struct {
	AccessToken string `yaml:"access_token"`
	DocIDs      DocIDs `yaml:"doc_ids"`
	Lang        string `yaml:"lang"`
	// Endpoint is only overridden by tests (fake GraphQL servers).
	Endpoint string `yaml:"-"`
}

// Defaults are the values of the 2026-10-21 WA Web snapshot.
func Defaults() Config {
	return Config{Endpoint: DefaultEndpoint, AccessToken: DefaultAccessToken, Lang: "pt_BR", DocIDs: DocIDs{
		Catalog: "30445081048424116", Product: "9660926520672123", Collections: "9430970660362540", Order: "26593811266898374"}}
}

// WithDefaults fills the zero fields of c.
func (c Config) WithDefaults() Config {
	d := Defaults()
	if c.Endpoint == "" {
		c.Endpoint = d.Endpoint
	}
	if c.AccessToken == "" {
		c.AccessToken = d.AccessToken
	}
	if c.Lang == "" {
		c.Lang = d.Lang
	}
	if c.DocIDs.Catalog == "" {
		c.DocIDs.Catalog = d.DocIDs.Catalog
	}
	if c.DocIDs.Product == "" {
		c.DocIDs.Product = d.DocIDs.Product
	}
	if c.DocIDs.Collections == "" {
		c.DocIDs.Collections = d.DocIDs.Collections
	}
	if c.DocIDs.Order == "" {
		c.DocIDs.Order = d.DocIDs.Order
	}
	return c
}

// Validate checks configured values (doc_ids are digits; the token has no spaces).
func (c Config) Validate() error {
	for name, id := range map[string]string{"catalog": c.DocIDs.Catalog, "product": c.DocIDs.Product,
		"collections": c.DocIDs.Collections, "order": c.DocIDs.Order} {
		if id != "" && (len(id) > 32 || strings.Trim(id, "0123456789") != "") {
			return fmt.Errorf("whatsapp_catalog.doc_ids.%s must be digits", name)
		}
	}
	if strings.ContainsAny(c.AccessToken, " \t\r\n") || len(c.AccessToken) > 256 {
		return errors.New("whatsapp_catalog.access_token is not valid")
	}
	if c.Lang != "" && (len(c.Lang) > 10 || strings.ContainsAny(c.Lang, " \t\r\n\"")) {
		return errors.New("whatsapp_catalog.lang is a locale such as pt_BR")
	}
	return nil
}

// Errors (match with errors.Is; *Error carries the detail).
var (
	ErrNotFound         = errors.New("catalog not found")
	ErrRateLimited      = errors.New("catalog rate limited")
	ErrUpstreamChanged  = errors.New("catalog upstream changed")
	ErrCommerceDisabled = errors.New("commerce disabled for this business")
	ErrUnavailable      = errors.New("catalog unavailable")
)

// Error is a failed read: Kind is one of the sentinel errors above.
type Error struct {
	Kind   error
	Detail string
}

func (e *Error) Error() string { return e.Kind.Error() + ": " + e.Detail }
func (e *Error) Unwrap() error { return e.Kind }

func fail(kind error, format string, args ...any) error {
	return &Error{Kind: kind, Detail: fmt.Sprintf(format, args...)}
}

// MaxResponse caps a GraphQL response body.
const MaxResponse = 2 << 20

// Doer sends HTTP requests (an *http.Client built by NewHTTPClient).
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// NewHTTPClient is the client of one instance's reads: an explicit proxy only (never the
// HTTP(S)_PROXY environment), a short timeout and no redirects.
func NewHTTPClient(proxyURL string) (*http.Client, error) {
	tr := &http.Transport{Proxy: nil, ResponseHeaderTimeout: 15 * time.Second, TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConnsPerHost: 2, IdleConnTimeout: 90 * time.Second}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("proxy: %w", err)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: tr, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// Client runs the read operations.
type Client struct {
	Config Config
}

// New returns a client with c's zero fields defaulted.
func New(c Config) *Client { return &Client{Config: c.WithDefaults()} }

// Product is a catalog product. Prices are integers in thousandths of the currency unit
// (priceAmount1000: R$ 12,50 is 12500).
type Product struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	PriceAmount1000 int64    `json:"price_amount_1000"`
	Currency        string   `json:"currency,omitempty"`
	SaleAmount1000  int64    `json:"sale_price_amount_1000,omitempty"`
	RetailerID      string   `json:"retailer_id,omitempty"`
	URL             string   `json:"url,omitempty"`
	Hidden          bool     `json:"hidden"`
	Availability    string   `json:"availability,omitempty"` // in_stock | out_of_stock | ...
	MaxAvailable    int64    `json:"max_available,omitempty"`
	ReviewStatus    string   `json:"review_status,omitempty"` // approved | pending | rejected | ...
	Images          []string `json:"images"`                  // original image URLs (CDN)
}

// Page is one page of a catalog.
type Page struct {
	Products []Product `json:"products"`
	After    string    `json:"after,omitempty"` // cursor of the next page ("" at the end)
}

// Collection is a group of products (with the first products of it).
type Collection struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	ReviewStatus string    `json:"review_status,omitempty"`
	Products     []Product `json:"products"`
}

// OrderItem and Order are the order details of the GraphQL fallback.
type OrderItem struct {
	ProductID       string `json:"product_id"`
	Name            string `json:"name"`
	PriceAmount1000 int64  `json:"price_amount_1000"`
	Currency        string `json:"currency,omitempty"`
	Quantity        int    `json:"quantity"`
	ImageURL        string `json:"image_url,omitempty"`
}

type Order struct {
	ID                 string      `json:"id"`
	CreatedAt          time.Time   `json:"created_at"`
	SubtotalAmount1000 int64       `json:"subtotal_amount_1000"`
	TotalAmount1000    int64       `json:"total_amount_1000"`
	Currency           string      `json:"currency,omitempty"`
	Items              []OrderItem `json:"items"`
}

// gqlError is one entry of "errors" (2xx) or the "error" object (non-2xx).
type gqlError struct {
	Code        int    `json:"code"`
	Message     string `json:"message"`
	Description string `json:"description"`
	Summary     string `json:"summary"`
}

// GraphQL error codes (research §2.0, §7).
const (
	codeUnauthorized    = 1675002
	codeRateLimit       = 1675004
	codeCommerceBlocked = 2498048
	codeNoCatalog       = 2498052
)

// query runs one persisted query and decodes data.<root> into out. A missing root is
// reported through found=false.
func (c *Client) query(ctx context.Context, d Doer, op, docID, root string, vars any, out any) (bool, error) {
	body, _ := json.Marshal(map[string]any{"access_token": c.Config.AccessToken, "doc_id": docID,
		"variables": vars, "lang": c.Config.Lang})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return false, fail(ErrUnavailable, "%s: %v", op, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := d.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, fail(ErrUnavailable, "%s: %v", op, ctx.Err())
		}
		return false, fail(ErrUnavailable, "%s: the catalog service could not be reached", op)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if err != nil {
		return false, fail(ErrUnavailable, "%s: reading the answer: %v", op, err)
	}
	if len(raw) > MaxResponse {
		return false, fail(ErrUpstreamChanged, "%s: the answer exceeds %d bytes", op, MaxResponse)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return false, fail(ErrRateLimited, "%s: HTTP 429", op)
	}
	var env struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []gqlError                 `json:"errors"`
		Error  *gqlError                  `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		if resp.StatusCode >= 500 {
			return false, fail(ErrUnavailable, "%s: HTTP %d", op, resp.StatusCode)
		}
		return false, fail(ErrUpstreamChanged, "%s: the answer is not JSON (HTTP %d)", op, resp.StatusCode)
	}
	errs := env.Errors
	if env.Error != nil {
		errs = append(errs, *env.Error)
	}
	for _, e := range errs {
		if err := mapCode(op, e); err != nil {
			return false, err
		}
	}
	if resp.StatusCode >= 500 {
		return false, fail(ErrUnavailable, "%s: HTTP %d", op, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || len(errs) > 0 {
		msg := ""
		if len(errs) > 0 {
			msg = firstNonEmpty(errs[0].Message, errs[0].Description, errs[0].Summary)
		}
		return false, fail(ErrUpstreamChanged, "%s: HTTP %d %s (the doc_id %s may have rotated: update whatsapp_catalog.doc_ids)",
			op, resp.StatusCode, clip(msg, 200), docID)
	}
	data, ok := env.Data[root]
	if !ok {
		return false, fail(ErrUpstreamChanged, "%s: the answer has no %s (the doc_id %s may have rotated: update whatsapp_catalog.doc_ids)",
			op, root, docID)
	}
	if string(data) == "null" {
		return false, nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, fail(ErrUpstreamChanged, "%s: unexpected %s shape: %v", op, root, err)
	}
	return true, nil
}

func mapCode(op string, e gqlError) error {
	switch e.Code {
	case codeUnauthorized, 190:
		return fail(ErrUpstreamChanged, "%s: the catalog access token was refused (code %d): update whatsapp_catalog.access_token", op, e.Code)
	case codeRateLimit, 4, 17, 32, 613:
		return fail(ErrRateLimited, "%s: code %d", op, e.Code)
	case codeCommerceBlocked:
		return fail(ErrCommerceDisabled, "%s: commerce is disabled for this business (code %d)", op, e.Code)
	case codeNoCatalog:
		return fail(ErrNotFound, "%s: this business has no catalog (code %d)", op, e.Code)
	}
	return nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// wire is a product as the GraphQL answers it (recorded shape, research §2.1). Numbers
// may come as strings or numbers.
type wire struct {
	ID           flexString `json:"id"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Price        flexString `json:"price"`
	Currency     string     `json:"currency"`
	RetailerID   string     `json:"retailer_id"`
	URL          string     `json:"url"`
	IsHidden     flexString `json:"is_hidden"`
	Availability string     `json:"availability"`
	MaxAvailable flexString `json:"max_available"`
	SalePrice    *struct {
		Price flexString `json:"price"`
	} `json:"sale_price"`
	StatusInfo *struct {
		Status string `json:"status"`
	} `json:"status_info"`
	Media *struct {
		Images []struct {
			ID               flexString `json:"id"`
			OriginalImageURL string     `json:"original_image_url"`
			RequestImageURL  string     `json:"request_image_url"`
		} `json:"images"`
	} `json:"media"`
}

// flexString decodes a JSON string, number or bool as its text.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*f = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	*f = flexString(strings.TrimSpace(string(b)))
	return nil
}

// enum lowers a GraphQL enum ("ISHIDDEN_TRUE" -> "true", "IN_STOCK" -> "in_stock").
func enum(s, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(s, prefix))
}

// Parse-time caps on third-party product text (WhatsApp's own limits are lower).
const (
	maxName        = 200
	maxDescription = 5000
	maxURL         = 1024
	maxShort       = 100 // ids, retailer ids, enums
	maxImages      = 10
)

// clipRunes shortens s to n runes.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func (w wire) product() Product {
	p := Product{ID: clipRunes(string(w.ID), maxShort), Name: clipRunes(w.Name, maxName),
		Description: clipRunes(w.Description, maxDescription), Currency: clipRunes(w.Currency, 8),
		RetailerID: clipRunes(w.RetailerID, maxShort), Availability: clipRunes(enum(w.Availability, "AVAILABILITY_"), maxShort),
		Images: []string{}}
	if len(w.URL) <= maxURL {
		p.URL = w.URL // a longer link is dropped, not cut into a different one
	}
	p.PriceAmount1000, _ = strconv.ParseInt(string(w.Price), 10, 64)
	p.MaxAvailable, _ = strconv.ParseInt(string(w.MaxAvailable), 10, 64)
	switch h := enum(string(w.IsHidden), "ISHIDDEN_"); h {
	case "true", "1":
		p.Hidden = true
	}
	if w.SalePrice != nil {
		p.SaleAmount1000, _ = strconv.ParseInt(string(w.SalePrice.Price), 10, 64)
	}
	if w.StatusInfo != nil {
		p.ReviewStatus = clipRunes(enum(w.StatusInfo.Status, "STATUS_"), maxShort)
	}
	if w.Media != nil {
		for _, im := range w.Media.Images {
			u := firstNonEmpty(im.OriginalImageURL, im.RequestImageURL)
			if u != "" && len(u) <= 4096 && len(p.Images) < maxImages {
				p.Images = append(p.Images, u)
			}
		}
	}
	return p
}

// variants are the null variant fields every WA Web catalog request carries.
func withVariants(m map[string]any) map[string]any {
	m["variant_info_fields"], m["variant_thumbnail_height"], m["variant_thumbnail_width"] = nil, nil, nil
	m["width"], m["height"] = "100", "100"
	return m
}

// Catalog reads one page of jid's catalog (jid: "<number>@s.whatsapp.net"). A business
// without a catalog is ErrNotFound; an empty catalog is a page without products.
func (c *Client) Catalog(ctx context.Context, d Doer, jid string, limit int, after string) (Page, error) {
	req := withVariants(map[string]any{"jid": jid, "allow_shop_source": "ALLOWSHOPSOURCE_TRUE",
		"limit": strconv.Itoa(limit), "after": nilIfEmpty(after), "catalog_session_id": nil})
	var root struct {
		ProductCatalog *struct {
			Products []wire `json:"products"`
			Paging   *struct {
				After string `json:"after"`
			} `json:"paging"`
		} `json:"product_catalog"`
	}
	found, err := c.query(ctx, d, "catalog", c.Config.DocIDs.Catalog, "xwa_product_catalog_get_product_catalog",
		map[string]any{"request": map[string]any{"product_catalog": req}}, &root)
	if err != nil {
		return Page{}, err
	}
	if !found || root.ProductCatalog == nil {
		return Page{}, fail(ErrNotFound, "catalog: this business has no catalog")
	}
	page := Page{Products: []Product{}}
	for _, w := range root.ProductCatalog.Products {
		page.Products = append(page.Products, w.product())
	}
	if pg := root.ProductCatalog.Paging; pg != nil && len(root.ProductCatalog.Products) > 0 {
		page.After = pg.After
	}
	return page, nil
}

// Product reads one product of jid's catalog (ErrNotFound when it is not visible).
func (c *Client) Product(ctx context.Context, d Doer, jid, productID string) (Product, error) {
	req := withVariants(map[string]any{"jid": jid, "product_id": productID, "fetch_compliance_info": "false"})
	var root struct {
		ProductCatalog *struct {
			Product *wire `json:"product"`
		} `json:"product_catalog"`
	}
	found, err := c.query(ctx, d, "product", c.Config.DocIDs.Product, "xwa_product_catalog_get_product",
		map[string]any{"request": map[string]any{"product": req}}, &root)
	if err != nil {
		return Product{}, err
	}
	if !found || root.ProductCatalog == nil || root.ProductCatalog.Product == nil || root.ProductCatalog.Product.ID == "" {
		return Product{}, fail(ErrNotFound, "product: not found or not visible")
	}
	return root.ProductCatalog.Product.product(), nil
}

// Collections reads jid's collections with their first products.
func (c *Client) Collections(ctx context.Context, d Doer, jid string) ([]Collection, error) {
	req := withVariants(map[string]any{"biz_jid": jid, "collection_limit": "51", "item_limit": "51", "after": nil})
	var root struct {
		Collections []struct {
			ID         flexString `json:"id"`
			Name       string     `json:"name"`
			Products   []wire     `json:"products"`
			StatusInfo *struct {
				Status string `json:"status"`
			} `json:"status_info"`
		} `json:"collections"`
	}
	found, err := c.query(ctx, d, "collections", c.Config.DocIDs.Collections, "xwa_product_catalog_get_collections",
		map[string]any{"request": map[string]any{"collections": req}}, &root)
	if err != nil {
		return nil, err
	}
	out := []Collection{}
	if !found {
		return out, nil
	}
	for _, col := range root.Collections {
		v := Collection{ID: string(col.ID), Name: col.Name, Products: []Product{}}
		if col.StatusInfo != nil {
			v.ReviewStatus = enum(col.StatusInfo.Status, "STATUS_")
		}
		for _, w := range col.Products {
			v.Products = append(v.Products, w.product())
		}
		out = append(out, v)
	}
	return out, nil
}

// Order reads an order's details (sellerJID: the business that received it).
func (c *Client) Order(ctx context.Context, d Doer, sellerJID, orderID, token string) (Order, error) {
	req := map[string]any{"jid": sellerJID, "id": orderID, "token": map[string]any{"sensitive_string_value": token},
		"image_dimensions": map[string]any{"height": "100", "width": "100"}}
	var root struct {
		Order *struct {
			ID           flexString `json:"id"`
			CreationTime flexString `json:"creation_time"`
			CreationTS   flexString `json:"creation_ts"`
			Price        *struct {
				Subtotal flexString `json:"subtotal"`
				Total    flexString `json:"total"`
				Currency string     `json:"currency"`
			} `json:"price"`
			Products []struct {
				ID       flexString `json:"id"`
				Name     string     `json:"name"`
				Price    flexString `json:"price"`
				Currency string     `json:"currency"`
				Quantity flexString `json:"quantity"`
				Image    *struct {
					URL string `json:"url"`
				} `json:"image"`
			} `json:"products"`
		} `json:"order"`
	}
	found, err := c.query(ctx, d, "order", c.Config.DocIDs.Order, "xwa_checkout_get_order_info",
		map[string]any{"request": map[string]any{"order": req}}, &root)
	if err != nil {
		return Order{}, err
	}
	if !found || root.Order == nil {
		return Order{}, fail(ErrNotFound, "order: not found")
	}
	o := root.Order
	out := Order{ID: string(o.ID), Items: []OrderItem{}}
	if ts, err := strconv.ParseInt(firstNonEmpty(string(o.CreationTS), string(o.CreationTime)), 10, 64); err == nil && ts > 0 {
		out.CreatedAt = time.Unix(ts, 0).UTC()
	}
	if p := o.Price; p != nil {
		out.SubtotalAmount1000, _ = strconv.ParseInt(string(p.Subtotal), 10, 64)
		out.TotalAmount1000, _ = strconv.ParseInt(string(p.Total), 10, 64)
		out.Currency = p.Currency
	}
	for _, it := range o.Products {
		item := OrderItem{ProductID: string(it.ID), Name: it.Name, Currency: it.Currency}
		item.PriceAmount1000, _ = strconv.ParseInt(string(it.Price), 10, 64)
		item.Quantity, _ = strconv.Atoi(string(it.Quantity))
		if it.Image != nil {
			item.ImageURL = it.Image.URL
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// MaxImage caps a downloaded product image.
const MaxImage = 5 << 20

// ImageHostAllowed reports whether a product image URL may be downloaded: https on
// Meta's media CDNs only (the URL comes from an upstream answer; nothing else is fetched).
func ImageHostAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, suffix := range []string{".whatsapp.net", ".fbcdn.net"} {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return false
}

// FetchImage downloads a product image (ImageHostAllowed hosts only, at most MaxImage).
func FetchImage(ctx context.Context, d Doer, raw string) ([]byte, string, error) {
	if !ImageHostAllowed(raw) {
		return nil, "", fail(ErrUpstreamChanged, "image: unexpected image host")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", fail(ErrUnavailable, "image: %v", err)
	}
	resp, err := d.Do(req)
	if err != nil {
		return nil, "", fail(ErrUnavailable, "image: the image could not be downloaded")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return nil, "", fail(ErrNotFound, "image: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fail(ErrUnavailable, "image: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxImage+1))
	if err != nil {
		return nil, "", fail(ErrUnavailable, "image: %v", err)
	}
	if len(b) > MaxImage {
		return nil, "", fail(ErrUnavailable, "image: larger than %d bytes", MaxImage)
	}
	mime := http.DetectContentType(b)
	if mime != "image/jpeg" && mime != "image/png" && mime != "image/webp" {
		return nil, "", fail(ErrUnavailable, "image: not a JPEG, PNG or WebP image")
	}
	return b, mime, nil
}

// Link is the shareable catalog link of a phone number (WA Web shares a catalog as
// https://wa.me/c/<number> in a text with link preview).
func Link(number string) string {
	if number == "" || strings.Trim(number, "0123456789") != "" {
		return ""
	}
	return "https://wa.me/c/" + number
}

// FormatBRL formats a thousandths amount as Brazilian reais ("R$ 1.234,50").
func FormatBRL(amount1000 int64) string {
	neg := amount1000 < 0
	if neg {
		amount1000 = -amount1000
	}
	cents := (amount1000 + 5) / 10
	reais, c := cents/100, cents%100
	s := strconv.FormatInt(reais, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	out := fmt.Sprintf("R$ %s,%02d", b.String(), c)
	if neg {
		out = "-" + out
	}
	return out
}
