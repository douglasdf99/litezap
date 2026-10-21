package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
)

func TestOrderReceived(t *testing.T) {
	in, st := setup(t)
	pub := &recPub{}
	in.Events = pub
	order := &workerv1.Message{Id: "O1", Chat: chat, Sender: chat, PushName: "Ana", TimestampMs: ts, Type: "order", Text: "sem cebola",
		Order: &workerv1.Order{OrderId: "1234567890", Token: "c2VjcmV0", SellerJid: "5511999990000@s.whatsapp.net", ItemCount: 3,
			Title: "Loja X", Message: "sem cebola", TotalAmount_1000: 119800, Currency: "BRL", Status: "inquiry"}}
	in.WorkerEvent("i1", msgEv(order))
	in.WorkerEvent("i1", msgEv(order)) // duplicate: nothing new
	evs := pub.snapshot()
	var types []string
	for _, e := range evs {
		types = append(types, e.typ)
	}
	if want := []string{events.TypeMessageReceived, events.TypeOrderReceived}; fmt.Sprint(types) != fmt.Sprint(want) {
		t.Fatalf("types = %v, want %v", types, want)
	}
	or := evs[1].data.(events.OrderReceived)
	if or.OrderID != "1234567890" || or.ItemCount != 3 || or.Total != 119.8 || or.Currency != "BRL" || or.Seller != "5511999990000@s.whatsapp.net" ||
		or.Chat != chat || or.MessageID == "" || or.Note != "sem cebola" || or.PushName != "Ana" {
		t.Fatalf("order.received = %+v", or)
	}
	// The token is kept server-side, never in events or the logged message.
	for _, e := range evs {
		b, _ := json.Marshal(e.data)
		if strings.Contains(string(b), "c2VjcmV0") {
			t.Fatalf("%s leaks the token: %s", e.typ, b)
		}
	}
	msg := evs[0].data.(events.Message)
	if msg.Type != "order" || msg.Order == nil || msg.Order.OrderID != "1234567890" || msg.Order.TotalAmount1000 != 119800 {
		t.Fatalf("message = %+v", msg)
	}
	o, err := st.GetOrder(context.Background(), "i1", "1234567890")
	if err != nil || o.Token != "c2VjcmV0" || o.MessageID != or.MessageID || o.Total1000 != 119800 {
		t.Fatalf("stored = %+v, %v", o, err)
	}
	list, err := st.RecentOrders(context.Background(), "i1", 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("recent = %v, %v", list, err)
	}
	// The account's own order message (from another device) is stored, but is no order.received.
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "O2", Chat: chat, FromMe: true, TimestampMs: ts, Type: "order",
		Order: &workerv1.Order{OrderId: "999", Token: "t"}}))
	if n := len(pub.snapshot()); n != 3 { // + message.sent
		t.Fatalf("events = %d", n)
	}
}

func TestProductMessageMeta(t *testing.T) {
	in, _ := setup(t)
	pub := &recPub{}
	in.Events = pub
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "P1", Chat: chat, Sender: chat, TimestampMs: ts, Type: "product", Text: "Bolo",
		Product: &workerv1.Product{ProductId: "77", Title: "Bolo", PriceAmount_1000: 25000, Currency: "BRL", BusinessOwnerJid: "5561@s.whatsapp.net"}}))
	msg := pub.snapshot()[0].data.(events.Message)
	if msg.Type != "product" || msg.Product == nil || msg.Product.ProductID != "77" || msg.Product.PriceAmount1000 != 25000 {
		t.Fatalf("product = %+v", msg)
	}
}
