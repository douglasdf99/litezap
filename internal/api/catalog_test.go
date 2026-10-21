package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/catalog/catalogtest"
	"github.com/douglasdf99/litezap/internal/store"
)

func TestCatalogRoutes(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Loja")
	_, otherTok := e.create("Outra")
	ctx := context.Background()
	if err := e.st.SetInstanceJID(ctx, id, "5511999990000:4@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	base := "/v1/instances/" + id
	errCode := func(out map[string]any) any { return out["error"].(map[string]any)["code"] }

	if code, out := e.do("GET", base+"/catalog", tok, ""); code != 404 || errCode(out) != "catalog_not_found" {
		t.Fatalf("no catalog = %d %v", code, out)
	}
	e.cat.Catalogs["5511999990000@s.whatsapp.net"] = []catalogtest.Product{{ID: "101", Name: "Camiseta", Price: 59900, Currency: "BRL"}}
	e.cat.Collections["5511999990000@s.whatsapp.net"] = map[string][]string{"Verão": {"101"}}
	// Another business's public catalog (the own one is still cached as missing for 5 minutes).
	e.cat.Catalogs["5511988887777@s.whatsapp.net"] = []catalogtest.Product{{ID: "7", Name: "Café", Price: 12500, Currency: "BRL"}}
	code, out := e.do("GET", base+"/catalog?jid=5511988887777&limit=10", tok, "")
	if code != 200 || out["catalog_link"] != "https://wa.me/c/5511988887777" || len(out["products"].([]any)) != 1 {
		t.Fatalf("catalog = %d %v", code, out)
	}
	if code, _ := e.do("GET", base+"/catalog?limit=x", tok, ""); code != 400 {
		t.Fatalf("bad limit = %d", code)
	}
	if code, _ := e.do("GET", base+"/catalog", otherTok, ""); code != 403 {
		t.Fatalf("other token = %d", code)
	}
	if code, out := e.do("GET", base+"/catalog/products/101", tok, ""); code != 200 || out["price_text"] != "R$ 59,90" {
		t.Fatalf("product = %d %v", code, out)
	}
	if code, out := e.do("GET", base+"/catalog/collections", tok, ""); code != 200 || len(out["collections"].([]any)) != 1 {
		t.Fatalf("collections = %d %v", code, out)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+base+"/catalog/products/101/image", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" || resp.Header.Get("X-Content-Type-Options") != "nosniff" || len(b) == 0 {
		t.Fatalf("image = %d %v", resp.StatusCode, resp.Header)
	}

	// Send a product through the queue.
	code, out = e.do("POST", base+"/messages?wait=true", tok, `{"to":"5511988887777","type":"product","product_id":"101","body":"Olha"}`)
	if code != 200 && code != 202 {
		t.Fatalf("send = %d %v", code, out)
	}
	e.eventually("product sent", func() bool {
		for _, r := range e.sp.Last(id).WA.Sent() {
			if r.Type == "product" && r.Product.GetProductId() == "101" && r.Product.GetBusinessOwnerJid() == "5511999990000@s.whatsapp.net" {
				return true
			}
		}
		return false
	})

	// Orders.
	if code, out := e.do("GET", base+"/orders/1", tok, ""); code != 404 || errCode(out) != "order_not_found" {
		t.Fatalf("unknown order = %d %v", code, out)
	}
	if _, err := e.st.UpsertOrder(ctx, id, store.Order{OrderID: "1", MessageID: "msg_x", ChatJID: "5511988887777@s.whatsapp.net",
		SellerJID: "5511999990000@s.whatsapp.net", Token: "c2VjcmV0", ItemCount: 1, Total1000: 59900, Currency: "BRL", ReceivedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	e.sp.Last(id).WA.SetOrder("1", &workerv1.OrderDetails{Id: "1", TotalAmount_1000: 59900, Currency: "BRL",
		Items: []*workerv1.OrderItem{{ProductId: "101", Name: "Camiseta", Quantity: 1, PriceAmount_1000: 59900}}})
	code, out = e.do("GET", base+"/orders/1", tok, "")
	if code != 200 || out["source"] != "socket" || len(out["items"].([]any)) != 1 {
		t.Fatalf("order = %d %v", code, out)
	}
	code, out = e.do("GET", base+"/orders", tok, "")
	if code != 200 || len(out["orders"].([]any)) != 1 {
		t.Fatalf("orders = %d %v", code, out)
	}
	if _, body := e.raw("GET", base+"/orders", tok); strings.Contains(body, "c2VjcmV0") {
		t.Fatal("the order list leaks the token")
	}
}
