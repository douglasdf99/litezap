package mcp

import (
	"context"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/catalog/catalogtest"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
)

func TestCatalogTools(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("C")
			ctx := context.Background()
			if err := e.msg.Store.SetInstanceJID(ctx, id, "5511999990000:2@s.whatsapp.net"); err != nil {
				t.Fatal(err)
			}
			cs := e.client(m, tok)
			fails(t, cs, "get_catalog", nil, "catalog_not_found")
			e.cat.Catalogs["5511988887777@s.whatsapp.net"] = []catalogtest.Product{{ID: "7", Name: "Café", Price: 12500, Currency: "BRL"}}
			var v service.CatalogView
			ok(t, cs, "get_catalog", map[string]any{"jid": "5511988887777"}, &v)
			if len(v.Products) != 1 || v.Products[0].PriceText != "R$ 12,50" || v.CatalogLink != "https://wa.me/c/5511988887777" {
				t.Fatalf("catalog = %+v", v)
			}
			var p service.CatalogProductView
			ok(t, cs, "get_product", map[string]any{"jid": "5511988887777", "product_id": "7"}, &p)
			if p.Name != "Café" {
				t.Fatalf("product = %+v", p)
			}
			fails(t, cs, "get_product", map[string]any{"product_id": "x"}, "product_id")

			var out sendOutput
			ok(t, cs, "send_product", map[string]any{"to": "5511977776666", "product_id": "7", "catalog_owner": "5511988887777",
				"body": "Recomendo"}, &out)
			if !out.Sent || out.Message.Type != "product" {
				t.Fatalf("send = %+v", out)
			}
			r := e.sp.Last(id).WA.Sent()
			if last := r[len(r)-1]; last.Product.GetBusinessOwnerJid() != "5511988887777@s.whatsapp.net" || last.Product.GetBody() != "Recomendo" {
				t.Fatalf("request = %+v", last)
			}
			fails(t, cs, "send_product", map[string]any{"to": "5511977776666"}, "product_id")

			fails(t, cs, "get_order", map[string]any{"order_id": "1"}, "order_not_found")
			if _, err := e.msg.Store.UpsertOrder(ctx, id, store.Order{OrderID: "1", SellerJID: "5511999990000@s.whatsapp.net", Token: "t",
				Total1000: 1000, Currency: "BRL", ReceivedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			e.sp.Last(id).WA.SetOrder("1", &workerv1.OrderDetails{Id: "1", TotalAmount_1000: 1000, Currency: "BRL"})
			var o service.OrderDetailsView
			ok(t, cs, "get_order", map[string]any{"order_id": "1"}, &o)
			if o.Source != "socket" || o.Total != 1 {
				t.Fatalf("order = %+v", o)
			}
		})
	}
}
