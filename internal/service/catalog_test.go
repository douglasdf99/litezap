package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/catalog"
	"github.com/douglasdf99/litezap/internal/catalog/catalogtest"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/store"
)

const ownJID = "5511999990000@s.whatsapp.net"

// withCatalog gives the env a catalog reader over a fake GraphQL endpoint; the instance is
// paired as 5511999990000 with a proxy.
func withCatalog(t *testing.T, e *socialEnv) *catalogtest.Fake {
	t.Helper()
	f := catalogtest.New()
	e.svc.Catalog = &CatalogReader{Client: f.Client(), Store: e.st, Defaults: fast(),
		HTTP: func(p string) (catalog.Doer, error) { return f.Doer(p), nil }}
	ctx := context.Background()
	if err := e.st.SetInstanceJID(ctx, "i1", "5511999990000:7@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetInstanceSettings(ctx, "i1", `{"proxy_url":"socks5://user:pw@10.0.0.1:1080"}`); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestCatalogReads(t *testing.T) {
	e := newSocialEnv(t)
	f := withCatalog(t, e)
	ctx := context.Background()
	if _, err := e.s.Catalog(ctx, "i1", "", 0, ""); apiCode(err) != "catalog_not_found" {
		t.Fatalf("no catalog = %v", err)
	}
	f.Catalogs[ownJID] = []catalogtest.Product{{ID: "101", Name: "Camiseta", Price: 59900, Currency: "BRL"},
		{ID: "102", Name: "Bolo", Price: 25000, Currency: "BRL", Hidden: true, Status: "PENDING"}}
	f.Collections[ownJID] = map[string][]string{"Verão": {"101"}}
	e.svc.Catalog.Invalidate("i1")
	v, err := e.s.Catalog(ctx, "i1", "", 0, "")
	if err != nil || len(v.Products) != 2 || v.Owner != ownJID || v.CatalogLink != "https://wa.me/c/5511999990000" || v.Cached {
		t.Fatalf("catalog = %+v, %v", v, err)
	}
	p := v.Products[0]
	if p.Price != 59.9 || p.PriceText != "R$ 59,90" || p.ImagePath != "/v1/instances/i1/catalog/products/101/image?jid=5511999990000" ||
		!v.Products[1].Hidden || v.Products[1].ReviewStatus != "pending" {
		t.Fatalf("products = %+v", v.Products)
	}
	// Through the instance's proxy, and cached for 5 minutes.
	if f.Proxies[0] != "socks5://user:pw@10.0.0.1:1080" {
		t.Fatalf("proxy = %v", f.Proxies)
	}
	calls := f.Calls()
	if v, _ := e.s.Catalog(ctx, "i1", "", 0, ""); !v.Cached || f.Calls() != calls {
		t.Fatal("the second read must come from the cache")
	}
	e.svc.Catalog.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	if v, _ := e.s.Catalog(ctx, "i1", "", 0, ""); v.Cached || f.Calls() != calls+1 {
		t.Fatal("the cache expires after 5 minutes")
	}
	e.svc.Catalog.Now = nil

	if pv, err := e.s.CatalogProduct(ctx, "i1", "", "101"); err != nil || pv.Name != "Camiseta" {
		t.Fatalf("product = %+v, %v", pv, err)
	}
	if _, err := e.s.CatalogProduct(ctx, "i1", "", "999"); apiCode(err) != "product_not_found" {
		t.Fatalf("missing product = %v", err)
	}
	if _, err := e.s.CatalogProduct(ctx, "i1", "", "abc"); apiCode(err) != "validation_failed" {
		t.Fatalf("bad id = %v", err)
	}
	cols, err := e.s.CatalogCollections(ctx, "i1", "")
	if err != nil || len(cols.Collections) != 1 || cols.Collections[0].Name != "Verão" || len(cols.Collections[0].Products) != 1 {
		t.Fatalf("collections = %+v, %v", cols, err)
	}
	img, mime, err := e.s.CatalogProductImage(ctx, "i1", "", "101")
	if err != nil || mime != "image/jpeg" || len(img) == 0 {
		t.Fatalf("image = %d %s %v", len(img), mime, err)
	}
	// Another business's catalog, by phone number.
	f.Catalogs["5511988887777@s.whatsapp.net"] = []catalogtest.Product{{ID: "7", Name: "Café", Price: 12500, Currency: "BRL"}}
	if v, err := e.s.Catalog(ctx, "i1", "+55 11 98888-7777", 10, ""); err != nil || v.Products[0].Name != "Café" || v.CatalogLink != "https://wa.me/c/5511988887777" {
		t.Fatalf("other = %+v, %v", v, err)
	}
	if _, err := e.s.Catalog(ctx, "i1", "120363@g.us", 10, ""); apiCode(err) != "validation_failed" {
		t.Fatalf("group jid = %v", err)
	}
	if _, err := e.s.Catalog(ctx, "i1", "", 51, ""); apiCode(err) != "validation_failed" {
		t.Fatalf("limit = %v", err)
	}
}

func TestCatalogErrorsAndProxyRequired(t *testing.T) {
	e := newSocialEnv(t)
	f := withCatalog(t, e)
	ctx := context.Background()
	for body, code := range map[string]string{
		`{"errors":[{"code":1675004,"message":"RATE_LIMIT_EXCEEDED"}]}`: "rate_limited",
		`{"error":{"code":1675002,"message":"unauthorized"}}`:           "upstream_changed",
		`{"data":{"renamed_root":{}}}`:                                  "upstream_changed",
	} {
		f.FailBody = body
		e.svc.Catalog.Invalidate("i1")
		_, err := e.s.Catalog(ctx, "i1", "", 0, "")
		if apiCode(err) != code {
			t.Errorf("%s = %v, want %s", body, err, code)
		}
		if code == "upstream_changed" && !strings.Contains(err.Error(), "whatsapp_catalog") {
			t.Errorf("upstream_changed must say what to update: %v", err)
		}
	}
	f.FailBody = ""
	if err := e.st.SetInstanceSettings(ctx, "i1", `{}`); err != nil {
		t.Fatal(err)
	}
	e.svc.Catalog.RequireProxy = true
	if _, err := e.s.Catalog(ctx, "i1", "", 0, ""); apiCode(err) != "proxy_required" {
		t.Fatalf("no proxy = %v", err)
	}
}

func TestSendProduct(t *testing.T) {
	e := newSocialEnv(t)
	f := withCatalog(t, e)
	ctx := context.Background()
	f.Catalogs[ownJID] = []catalogtest.Product{{ID: "101", Name: "Camiseta", Description: "Algodão", Price: 59900, Currency: "BRL"},
		{ID: "102", Name: "Oculto", Price: 1000, Currency: "BRL", Hidden: true}, {ID: "103", Name: "Sem foto", NoImage: true}}
	v, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "product", ProductID: "101", Body: "Olha esta", Footer: "Loja X"}, 2*time.Second)
	if err != nil || v.Status != "sent" || v.Type != "product" {
		t.Fatalf("send = %+v, %v", v, err)
	}
	r := e.lastSent()
	pr := r.Product
	if r.Type != "product" || pr.GetProductId() != "101" || pr.GetTitle() != "Camiseta" || pr.GetPriceAmount_1000() != 59900 ||
		pr.GetBusinessOwnerJid() != ownJID || pr.GetBody() != "Olha esta" || pr.GetFooter() != "Loja X" || pr.GetImageCount() != 1 ||
		len(pr.GetThumbnail()) == 0 || r.Mime != "image/jpeg" {
		t.Fatalf("request = %+v", r)
	}
	if got := e.sp.Last("i1").WA.SentMedia(r.MessageId); len(got) == 0 || string(got[:2]) != "\xff\xd8" {
		t.Fatal("the product image must be staged for the worker")
	}
	m, err := e.svc.GetMessage(ctx, "i1", v.ID)
	if err != nil || m.Product == nil || m.Product.ProductID != "101" || m.Text != "Olha esta" {
		t.Fatalf("logged = %+v, %v", m, err)
	}
	for name, in := range map[string]SendInput{
		"no product":   {To: "5511988887777", Type: "product"},
		"hidden":       {To: "5511988887777", Type: "product", ProductID: "102"},
		"no image":     {To: "5511988887777", Type: "product", ProductID: "103"},
		"text":         {To: "5511988887777", Type: "product", ProductID: "101", Text: "x"},
		"long footer":  {To: "5511988887777", Type: "product", ProductID: "101", Footer: strings.Repeat("a", 61)},
		"body on text": {To: "5511988887777", Type: "text", Text: "x", Body: "y"},
	} {
		if _, err := e.svc.Send(ctx, "i1", in, 0); apiCode(err) != "validation_failed" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "product", ProductID: "999"}, 0); apiCode(err) != "product_not_found" {
		t.Fatalf("missing = %v", err)
	}
	// The payload keeps the product for the queue.
	var p queue.Payload
	o, _ := e.st.GetOutbound(ctx, "i1", v.ID)
	_ = json.Unmarshal([]byte(o.Payload), &p)
	if p.Product == nil || p.Product.ProductID != "101" {
		t.Fatalf("payload = %+v", p)
	}
}

func TestOrderDetailsFallback(t *testing.T) {
	e := newSocialEnv(t)
	f := withCatalog(t, e)
	ctx := context.Background()
	if _, err := e.s.Order(ctx, "i1", "1234567890"); apiCode(err) != "order_not_found" {
		t.Fatalf("unknown order = %v", err)
	}
	if _, err := e.st.UpsertOrder(ctx, "i1", store.Order{OrderID: "1234567890", MessageID: "msg_1", ChatJID: "5511988887777@s.whatsapp.net",
		SellerJID: ownJID, Token: "c2VjcmV0", ItemCount: 2, Total1000: 50000, Currency: "BRL", ReceivedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// The socket IQ answers first.
	e.wa().SetOrder("1234567890", &workerv1.OrderDetails{Id: "1234567890", TotalAmount_1000: 50000, Currency: "BRL",
		Items: []*workerv1.OrderItem{{ProductId: "101", Name: "Camiseta", Quantity: 2, PriceAmount_1000: 25000, Currency: "BRL"}}})
	d, err := e.s.Order(ctx, "i1", "1234567890")
	if err != nil || d.Source != "socket" || len(d.Items) != 1 || d.Items[0].Price != 25 || d.TotalText != "R$ 50,00" {
		t.Fatalf("socket = %+v, %v", d, err)
	}
	if toks := e.wa().OrderTokens(); len(toks) != 1 || toks[0] != "c2VjcmV0" {
		t.Fatalf("tokens = %v", toks)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "c2VjcmV0") {
		t.Fatalf("the view leaks the token: %s", b)
	}
	// The IQ fails: GraphQL.
	e.wa().SetOrder("1234567890", nil)
	f.Orders["1234567890"] = `{"id":"1234567890","creation_ts":"1800000000","price":{"subtotal":"50000","total":"50000","currency":"BRL"},
		"products":[{"id":"101","name":"Camiseta","price":"25000","currency":"BRL","quantity":"2"}]}`
	d, err = e.s.Order(ctx, "i1", "1234567890")
	if err != nil || d.Source != "graphql" || len(d.Items) != 1 || d.Items[0].Quantity != 2 || d.CreatedAt == nil {
		t.Fatalf("graphql = %+v, %v", d, err)
	}
	list, err := e.s.Orders(ctx, "i1", 0)
	if err != nil || len(list) != 1 || list[0].Total != 50 {
		t.Fatalf("orders = %+v, %v", list, err)
	}
}
