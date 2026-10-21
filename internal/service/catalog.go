package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/catalog"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
)

// ---- WhatsApp Business catalog, phase 1 (marco 12): reads only ----
//
// Reads use the public catalog GraphQL (internal/catalog). They run in the supervisor,
// not in the worker, but through the instance's own proxy (its effective proxy_url, the
// same one its worker uses for the websocket and media): every request about a number
// leaves from that number's IP, so the catalog endpoint never sees one IP fetching many
// numbers' catalogs, and a read still works while the worker restarts. Without a proxy
// the read is direct, like the worker itself; with require_proxy and no proxy it is
// refused (never a direct connection).
//
// The endpoint rate-limits by IP: answers are cached for 5 minutes and uncached reads
// are limited per instance.

const (
	catalogCacheTTL     = 5 * time.Minute
	catalogCacheMax     = 512
	defaultCatalogLimit = 30
	maxCatalogLimit     = 50
	catalogRPCTimeout   = 25 * time.Second
	maxProductIDLen     = 64
)

// Upstream calls per instance (cache misses only).
var (
	catalogQuota = []window{{time.Minute, 30}, {24 * time.Hour, 1000}}
	imageQuota   = []window{{time.Minute, 120}, {24 * time.Hour, 3000}}
)

// CatalogReader reads catalogs through each instance's proxy, with a cache.
type CatalogReader struct {
	Client       *catalog.Client
	Store        *store.Store
	Defaults     settings.Settings
	DefaultProxy string
	// RequireProxy refuses reads for instances without a proxy (config require_proxy).
	RequireProxy bool
	// HTTP builds the client of a proxy URL ("" = direct); nil: catalog.NewHTTPClient.
	HTTP func(proxyURL string) (catalog.Doer, error)
	Now  func() time.Time

	mu      sync.Mutex
	cache   map[string]catalogEntry
	clients map[string]catalog.Doer // by proxy URL
	quota   windowQuota
}

type catalogEntry struct {
	at time.Time
	v  any
}

func (r *CatalogReader) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// doer returns the HTTP client of the instance's effective proxy.
func (r *CatalogReader) doer(in store.Instance) (catalog.Doer, error) {
	set, err := settings.Effective(r.Defaults, r.DefaultProxy, in.SettingsJSON)
	if err != nil {
		return nil, err
	}
	if r.RequireProxy && set.ProxyURL == "" {
		return nil, apierr.New(http.StatusConflict, "proxy_required", "this server requires a proxy for every instance", false)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.clients[set.ProxyURL]; ok {
		return d, nil
	}
	mk := r.HTTP
	if mk == nil {
		mk = func(p string) (catalog.Doer, error) { return catalog.NewHTTPClient(p) }
	}
	d, err := mk(set.ProxyURL)
	if err != nil {
		return nil, apierr.Validation("proxy_url is not valid")
	}
	if r.clients == nil {
		r.clients = map[string]catalog.Doer{}
	}
	r.clients[set.ProxyURL] = d
	return d, nil
}

func (r *CatalogReader) cached(key string) (any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.cache[key]
	if !ok || r.now().Sub(e.at) >= catalogCacheTTL {
		return nil, false
	}
	return e.v, true
}

func (r *CatalogReader) store(key string, v any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil || len(r.cache) >= catalogCacheMax {
		now := r.now()
		next := map[string]catalogEntry{}
		for k, e := range r.cache { // keep the fresh entries
			if now.Sub(e.at) < catalogCacheTTL && len(next) < catalogCacheMax/2 {
				next[k] = e
			}
		}
		r.cache = next
	}
	r.cache[key] = catalogEntry{at: r.now(), v: v}
}

// Invalidate drops the cached answers of an instance (tests, and after changes).
func (r *CatalogReader) Invalidate(instanceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.cache {
		if strings.HasPrefix(k, instanceID+"\x00") {
			delete(r.cache, k)
		}
	}
}

// catalogErr renders a catalog failure as an API error.
func catalogErr(err error, what string) error {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return err
	}
	var ce *catalog.Error
	detail := ""
	if errors.As(err, &ce) {
		detail = ce.Detail
	}
	switch {
	case errors.Is(err, catalog.ErrNotFound):
		if what == "product" {
			return apierr.New(http.StatusNotFound, "product_not_found", "product not found in this catalog (or hidden)", false)
		}
		if what == "order" {
			return apierr.New(http.StatusNotFound, "order_not_found", "order not found", false)
		}
		return apierr.New(http.StatusNotFound, "catalog_not_found", "this number has no WhatsApp catalog", false)
	case errors.Is(err, catalog.ErrRateLimited):
		return apierr.New(http.StatusTooManyRequests, "rate_limited", "the WhatsApp catalog service is rate limiting; try again in a few minutes", true)
	case errors.Is(err, catalog.ErrCommerceDisabled):
		return apierr.New(http.StatusConflict, "commerce_disabled", "WhatsApp disabled commerce for this business (region or policy)", false)
	case errors.Is(err, catalog.ErrUpstreamChanged):
		return apierr.New(http.StatusBadGateway, "upstream_changed",
			"WhatsApp changed its catalog service ("+redact(detail)+"); update whatsapp_catalog in the config or LiteZap", false)
	case errors.Is(err, catalog.ErrUnavailable):
		return apierr.New(http.StatusServiceUnavailable, "catalog_unavailable", "the WhatsApp catalog service could not be reached: "+redact(detail), true)
	}
	return err
}

// owner resolves whose catalog to read: jidRaw (a phone number or phone JID) or, empty,
// the instance's own number. It returns the JID and the bare number.
func catalogOwner(in store.Instance, jidRaw string) (string, string, error) {
	if strings.TrimSpace(jidRaw) == "" {
		user, _, _ := strings.Cut(in.JID, "@")
		user, _, _ = strings.Cut(user, ":")
		if user == "" {
			return "", "", apierr.New(http.StatusConflict, "instance_not_connected", "pair the number first (its own catalog is read by its phone number)", false)
		}
		return user + "@s.whatsapp.net", user, nil
	}
	j, err := ChatJID(jidRaw)
	if err != nil {
		return "", "", err
	}
	if !strings.HasSuffix(j, "@s.whatsapp.net") {
		return "", "", apierr.Validation("jid must be a phone number or a phone JID (…@s.whatsapp.net)")
	}
	user, _, _ := strings.Cut(j, "@")
	return j, user, nil
}

func validProductID(id string) bool {
	return id != "" && len(id) <= maxProductIDLen && strings.Trim(id, "0123456789") == ""
}

// CatalogProductView is a product as the API shows it. Prices: price_amount_1000 is the
// integer WhatsApp uses (thousandths of the unit), price the same in units.
type CatalogProductView struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Description     string  `json:"description,omitempty"`
	Price           float64 `json:"price"`
	PriceAmount1000 int64   `json:"price_amount_1000"`
	Currency        string  `json:"currency,omitempty"`
	PriceText       string  `json:"price_text,omitempty"` // BRL only: "R$ 59,90"
	SalePrice       float64 `json:"sale_price,omitempty"`
	SaleAmount1000  int64   `json:"sale_price_amount_1000,omitempty"`
	RetailerID      string  `json:"retailer_id,omitempty"`
	URL             string  `json:"url,omitempty"`
	Hidden          bool    `json:"hidden"`
	Availability    string  `json:"availability,omitempty"`
	ReviewStatus    string  `json:"review_status,omitempty"`
	ImageCount      int     `json:"image_count"`
	// ImageURL is WhatsApp's CDN link (signed, it expires); ImagePath downloads the first
	// image through LiteZap (and the instance's proxy).
	ImageURL  string `json:"image_url,omitempty"`
	ImagePath string `json:"image_path,omitempty"`
}

func productView(instanceID, owner string, p catalog.Product) CatalogProductView {
	v := CatalogProductView{ID: p.ID, Name: p.Name, Description: p.Description, Price: float64(p.PriceAmount1000) / 1000,
		PriceAmount1000: p.PriceAmount1000, Currency: p.Currency, SalePrice: float64(p.SaleAmount1000) / 1000,
		SaleAmount1000: p.SaleAmount1000, RetailerID: p.RetailerID, URL: p.URL, Hidden: p.Hidden,
		Availability: p.Availability, ReviewStatus: p.ReviewStatus, ImageCount: len(p.Images)}
	if p.Currency == "BRL" && p.PriceAmount1000 > 0 {
		v.PriceText = catalog.FormatBRL(p.PriceAmount1000)
	}
	if len(p.Images) > 0 {
		v.ImageURL = p.Images[0]
		v.ImagePath = "/v1/instances/" + instanceID + "/catalog/products/" + p.ID + "/image"
		if user, _, _ := strings.Cut(owner, "@"); user != "" {
			v.ImagePath += "?jid=" + user
		}
	}
	return v
}

// CatalogView is a page of a catalog.
type CatalogView struct {
	Owner string `json:"owner"`
	// CatalogLink shares the whole catalog: send it as a text (it gets a link preview).
	CatalogLink string               `json:"catalog_link"`
	Products    []CatalogProductView `json:"products"`
	NextCursor  string               `json:"next_cursor,omitempty"`
	Cached      bool                 `json:"cached"`
}

// CollectionView is a catalog collection with its first products.
type CollectionView struct {
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	ReviewStatus string               `json:"review_status,omitempty"`
	Products     []CatalogProductView `json:"products"`
}

// CollectionsView lists a catalog's collections.
type CollectionsView struct {
	Owner       string           `json:"owner"`
	CatalogLink string           `json:"catalog_link"`
	Collections []CollectionView `json:"collections"`
}

// call runs one upstream read for an instance, from the cache when possible.
func (r *CatalogReader) call(ctx context.Context, instanceID, kind, key string, quota []window,
	fn func(context.Context, catalog.Doer) (any, error)) (any, bool, error) {
	ck := instanceID + "\x00" + kind + "\x00" + key
	if v, ok := r.cached(ck); ok {
		return v, true, nil
	}
	in, err := r.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, false, mapErr(err)
	}
	d, err := r.doer(in)
	if err != nil {
		return nil, false, err
	}
	if !r.quota.take(kind+"\x00"+instanceID, r.now(), quota...) {
		return nil, false, socialLimited("catalog read")
	}
	c, cancel := context.WithTimeout(ctx, catalogRPCTimeout)
	defer cancel()
	v, err := fn(c, d)
	if err != nil {
		return nil, false, err
	}
	r.store(ck, v)
	return v, false, nil
}

func (r *CatalogReader) instance(ctx context.Context, instanceID string) (store.Instance, error) {
	if r == nil || r.Client == nil {
		return store.Instance{}, apierr.New(http.StatusNotImplemented, "not_implemented", "the WhatsApp catalog is not available on this server", false)
	}
	in, err := r.Store.GetInstance(ctx, instanceID)
	return in, mapErr(err)
}

// Page reads one page of a catalog (jidRaw "": the instance's own).
func (r *CatalogReader) Page(ctx context.Context, instanceID, jidRaw string, limit int, cursor string) (CatalogView, error) {
	in, err := r.instance(ctx, instanceID)
	if err != nil {
		return CatalogView{}, err
	}
	owner, number, err := catalogOwner(in, jidRaw)
	if err != nil {
		return CatalogView{}, err
	}
	if limit <= 0 {
		limit = defaultCatalogLimit
	}
	if limit > maxCatalogLimit || len(cursor) > 1024 {
		return CatalogView{}, apierr.Validation("limit is 1 to 50 and cursor the next_cursor of the previous page")
	}
	v, cached, err := r.call(ctx, instanceID, "catalog", owner+"|"+strconv.Itoa(limit)+"|"+cursor, catalogQuota,
		func(c context.Context, d catalog.Doer) (any, error) {
			return r.Client.Catalog(c, d, owner, limit, cursor)
		})
	if err != nil {
		return CatalogView{}, catalogErr(err, "catalog")
	}
	page := v.(catalog.Page)
	out := CatalogView{Owner: owner, CatalogLink: catalog.Link(number), Products: []CatalogProductView{},
		NextCursor: page.After, Cached: cached}
	for _, p := range page.Products {
		out.Products = append(out.Products, productView(instanceID, owner, p))
	}
	return out, nil
}

// rawProduct reads one product (cached).
func (r *CatalogReader) rawProduct(ctx context.Context, instanceID, owner, productID string) (catalog.Product, error) {
	if !validProductID(productID) {
		return catalog.Product{}, apierr.Validation("product_id is the numeric id of a catalog product")
	}
	v, _, err := r.call(ctx, instanceID, "product", owner+"|"+productID, catalogQuota,
		func(c context.Context, d catalog.Doer) (any, error) { return r.Client.Product(c, d, owner, productID) })
	if err != nil {
		return catalog.Product{}, catalogErr(err, "product")
	}
	return v.(catalog.Product), nil
}

// Product reads one product of a catalog.
func (r *CatalogReader) Product(ctx context.Context, instanceID, jidRaw, productID string) (CatalogProductView, error) {
	in, err := r.instance(ctx, instanceID)
	if err != nil {
		return CatalogProductView{}, err
	}
	owner, _, err := catalogOwner(in, jidRaw)
	if err != nil {
		return CatalogProductView{}, err
	}
	p, err := r.rawProduct(ctx, instanceID, owner, productID)
	if err != nil {
		return CatalogProductView{}, err
	}
	return productView(instanceID, owner, p), nil
}

// Collections lists a catalog's collections.
func (r *CatalogReader) Collections(ctx context.Context, instanceID, jidRaw string) (CollectionsView, error) {
	in, err := r.instance(ctx, instanceID)
	if err != nil {
		return CollectionsView{}, err
	}
	owner, number, err := catalogOwner(in, jidRaw)
	if err != nil {
		return CollectionsView{}, err
	}
	v, _, err := r.call(ctx, instanceID, "collections", owner, catalogQuota,
		func(c context.Context, d catalog.Doer) (any, error) { return r.Client.Collections(c, d, owner) })
	if err != nil {
		return CollectionsView{}, catalogErr(err, "catalog")
	}
	out := CollectionsView{Owner: owner, CatalogLink: catalog.Link(number), Collections: []CollectionView{}}
	for _, col := range v.([]catalog.Collection) {
		cv := CollectionView{ID: col.ID, Name: col.Name, ReviewStatus: col.ReviewStatus, Products: []CatalogProductView{}}
		for _, p := range col.Products {
			cv.Products = append(cv.Products, productView(instanceID, owner, p))
		}
		out.Collections = append(out.Collections, cv)
	}
	return out, nil
}

// ProductImage downloads a product's first image through the instance's proxy.
func (r *CatalogReader) ProductImage(ctx context.Context, instanceID, jidRaw, productID string) ([]byte, string, error) {
	in, err := r.instance(ctx, instanceID)
	if err != nil {
		return nil, "", err
	}
	owner, _, err := catalogOwner(in, jidRaw)
	if err != nil {
		return nil, "", err
	}
	p, err := r.rawProduct(ctx, instanceID, owner, productID)
	if err != nil {
		return nil, "", err
	}
	return r.image(ctx, instanceID, in, p)
}

func (r *CatalogReader) image(ctx context.Context, instanceID string, in store.Instance, p catalog.Product) ([]byte, string, error) {
	if len(p.Images) == 0 {
		return nil, "", apierr.New(http.StatusNotFound, "not_found", "this product has no image", false)
	}
	d, err := r.doer(in)
	if err != nil {
		return nil, "", err
	}
	if !r.quota.take("image\x00"+instanceID, r.now(), imageQuota...) {
		return nil, "", socialLimited("catalog image")
	}
	c, cancel := context.WithTimeout(ctx, catalogRPCTimeout)
	defer cancel()
	b, mime, err := catalog.FetchImage(c, d, p.Images[0])
	if err != nil {
		return nil, "", catalogErr(err, "image")
	}
	return b, mime, nil
}

// Products reads up to max products of the instance's own catalog (all pages, cached),
// for the attendant's knowledge base.
func (r *CatalogReader) Products(ctx context.Context, instanceID string, max int) ([]catalog.Product, error) {
	in, err := r.instance(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	owner, _, err := catalogOwner(in, "")
	if err != nil {
		return nil, err
	}
	var out []catalog.Product
	cursor := ""
	for pages := 0; pages < 10 && len(out) < max; pages++ {
		cur := cursor
		v, _, err := r.call(ctx, instanceID, "catalog", owner+"|"+strconv.Itoa(maxCatalogLimit)+"|"+cur, catalogQuota,
			func(c context.Context, d catalog.Doer) (any, error) {
				return r.Client.Catalog(c, d, owner, maxCatalogLimit, cur)
			})
		if err != nil {
			return nil, catalogErr(err, "catalog")
		}
		page := v.(catalog.Page)
		out = append(out, page.Products...)
		if page.After == "" || page.After == cursor {
			break
		}
		cursor = page.After
	}
	if len(out) > max {
		out = out[:max]
	}
	return out, nil
}

// orderGraphQL reads an order's details with the GraphQL fallback.
func (r *CatalogReader) orderGraphQL(ctx context.Context, instanceID, seller, orderID, token string) (catalog.Order, error) {
	v, _, err := r.call(ctx, instanceID, "order", seller+"|"+orderID, catalogQuota,
		func(c context.Context, d catalog.Doer) (any, error) {
			return r.Client.Order(c, d, seller, orderID, token)
		})
	if err != nil {
		return catalog.Order{}, err
	}
	return v.(catalog.Order), nil
}
