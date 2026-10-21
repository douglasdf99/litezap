package worker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Explicit clears of the business profile (device validation pending: see bizprofile.go).
func TestBuildBusinessProfileClearsGolden(t *testing.T) {
	for strategy, want := range map[string]string{
		"":                         `<business_profile mutation_type="delta" v="3"><latitude/><longitude/></business_profile>`,
		LocationClearEmptyElements: `<business_profile mutation_type="delta" v="3"><latitude/><longitude/></business_profile>`,
		LocationClearEmptyContent:  `<business_profile mutation_type="delta" v="3"><latitude></latitude><longitude></longitude></business_profile>`,
		LocationClearEmptyAddress:  `<business_profile mutation_type="delta" v="3"><address/></business_profile>`,
	} {
		n, err := buildBusinessProfileSet(&workerv1.SetBusinessProfileRequest{ClearLocation: true, LocationClear: strategy})
		if err != nil || n.String() != want {
			t.Errorf("location clear %q = %s, %v\nwant %s", strategy, n.String(), err, want)
		}
	}
	n, err := buildBusinessProfileSet(&workerv1.SetBusinessProfileRequest{ClearHours: true, Description: proto.String("")})
	if want := `<business_profile mutation_type="delta" v="3"><description></description><business_hours/></business_profile>`; err != nil || n.String() != want {
		t.Fatalf("hours clear = %s, %v\nwant %s", n.String(), err, want)
	}
	for name, req := range map[string]*workerv1.SetBusinessProfileRequest{
		"set and clear location": {ClearLocation: true, SetLocation: true, Latitude: 1, Longitude: 1},
		"set and clear hours":    {ClearHours: true, Hours: &workerv1.BusinessHoursSet{Timezone: "UTC"}},
		"unknown strategy":       {ClearLocation: true, LocationClear: "nope"},
		"strategy without clear": {LocationClear: LocationClearEmptyContent, Description: proto.String("x")},
		"address with empty_address": {ClearLocation: true, LocationClear: LocationClearEmptyAddress,
			Address: proto.String("Rua A")},
	} {
		if _, err := buildBusinessProfileSet(req); err == nil {
			t.Errorf("%s: accepted", name)
		} else if c, code := codeOf(err); c != codes.InvalidArgument || code != "validation_failed" {
			t.Errorf("%s: %v %v", name, c, code)
		}
	}
}

func TestBuildProductMessageGolden(t *testing.T) {
	up := whatsmeow.UploadResponse{URL: "https://mmg.whatsapp.net/o1", DirectPath: "/v/t62/o1", MediaKey: []byte{1},
		FileEncSHA256: []byte{2}, FileSHA256: []byte{3}, FileLength: 1234}
	m := buildProductMessage(&workerv1.Product{ProductId: "8364718273645", Title: "Camiseta", Description: "Algodão",
		Currency: "BRL", PriceAmount_1000: 59900, SalePriceAmount_1000: 49900, RetailerId: "SKU-1", Url: "https://loja.x/c",
		ImageCount: 2, BusinessOwnerJid: "5511999990000@s.whatsapp.net", Body: "Olha esta", Footer: "Loja X",
		Thumbnail: []byte{0xff, 0xd8}}, "image/jpeg", up)
	got, _ := protojson.MarshalOptions{UseProtoNames: false}.Marshal(m)
	want := `{"productMessage":{"product":{"productImage":{"URL":"https://mmg.whatsapp.net/o1","mimetype":"image/jpeg",` +
		`"fileSHA256":"Aw==","fileLength":"1234","mediaKey":"AQ==","fileEncSHA256":"Ag==","directPath":"/v/t62/o1",` +
		`"JPEGThumbnail":"/9g="},"productID":"8364718273645","title":"Camiseta","description":"Algodão","currencyCode":"BRL",` +
		`"priceAmount1000":"59900","retailerID":"SKU-1","URL":"https://loja.x/c","productImageCount":2,"salePriceAmount1000":"49900"},` +
		`"businessOwnerJID":"5511999990000@s.whatsapp.net","body":"Olha esta","footer":"Loja X"}}`
	if !jsonEqual(t, got, want) {
		t.Fatalf("product message =\n%s\nwant\n%s", got, want)
	}
	// No price: neither price nor currency (WA Web nulls both when the price is 0).
	m = buildProductMessage(&workerv1.Product{ProductId: "1", Title: "Grátis", Currency: "BRL", BusinessOwnerJid: "5511@s.whatsapp.net"}, "image/png", up)
	if s := m.GetProductMessage().GetProduct(); s.CurrencyCode != nil || s.PriceAmount1000 != nil || s.SalePriceAmount1000 != nil ||
		m.GetProductMessage().Body != nil || m.GetProductMessage().Catalog != nil {
		t.Fatalf("free product = %v", m)
	}
}

func TestBuildProductUploadsImage(t *testing.T) {
	w, dir := composeAdapter(t)
	ctx := context.Background()
	p := &workerv1.Product{ProductId: "42", Title: "Bolo", BusinessOwnerJid: "5511999990000@s.whatsapp.net", Thumbnail: []byte{9}}
	m, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "product", Mime: "image/jpeg", MediaPath: stage(t, dir, "p1", []byte("jpeg")),
		Product: p, Quote: &workerv1.Quote{MessageId: "Q1", Text: "oi"}})
	pm := m.GetProductMessage()
	if err != nil || pm.GetProduct().GetProductImage().GetFileLength() != 4 || pm.GetProduct().GetProductID() != "42" ||
		pm.GetContextInfo().GetStanzaID() != "Q1" {
		t.Fatalf("product = %v, %v", m, err)
	}
	for name, req := range map[string]*workerv1.SendRequest{
		"no product": {Type: "product", MediaPath: stage(t, dir, "p2", []byte("x"))},
		"owner lid":  {Type: "product", MediaPath: stage(t, dir, "p3", []byte("x")), Product: &workerv1.Product{ProductId: "1", Title: "t", BusinessOwnerJid: "1@lid"}},
		"no image":   {Type: "product", MediaPath: dir + "/missing", Product: p},
	} {
		if _, err := w.build(ctx, dm, req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReceivedProductAndOrder(t *testing.T) {
	order := &waE2E.Message{OrderMessage: &waE2E.OrderMessage{OrderID: proto.String("1234567890"), Token: proto.String("dG9rZW4="),
		ItemCount: proto.Int32(3), Status: waE2E.OrderMessage_INQUIRY.Enum(), OrderTitle: proto.String("Loja X"),
		SellerJID: proto.String("5511999990000:3@s.whatsapp.net"), TotalAmount1000: proto.Int64(119800), TotalCurrencyCode: proto.String("BRL"),
		ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("P1")}}}
	typ, text, _, _, ok := content(order)
	if !ok || typ != "order" || text != "Loja X" {
		t.Fatalf("order content = %q %q %v", typ, text, ok)
	}
	out := &workerv1.Message{}
	details(order, out)
	o := out.Order
	if o == nil || o.OrderId != "1234567890" || o.Token != "dG9rZW4=" || o.ItemCount != 3 || o.Status != "inquiry" ||
		o.SellerJid != "5511999990000@s.whatsapp.net" || o.TotalAmount_1000 != 119800 || o.Currency != "BRL" || out.QuotedId != "P1" {
		t.Fatalf("order = %+v (quoted %q)", o, out.QuotedId)
	}

	prod := &waE2E.Message{ProductMessage: &waE2E.ProductMessage{BusinessOwnerJID: proto.String("5511999990000@s.whatsapp.net"),
		Product: &waE2E.ProductMessage_ProductSnapshot{ProductID: proto.String("77"), Title: proto.String("Bolo"),
			PriceAmount1000: proto.Int64(25000), CurrencyCode: proto.String("BRL")}}}
	typ, text, _, _, ok = content(prod)
	out = &workerv1.Message{}
	details(prod, out)
	if !ok || typ != "product" || text != "Bolo" || out.Product.GetProductId() != "77" || out.Product.GetPriceAmount_1000() != 25000 {
		t.Fatalf("product = %q %q %+v", typ, text, out.Product)
	}
}

func TestOrderDetails(t *testing.T) {
	w := newMsgAdapter(t)
	ctx := context.Background()
	var gotID, gotToken string
	var fail error
	w.orderDetails = func(_ context.Context, id, token string) (*types.OrderDetails, error) {
		gotID, gotToken = id, token
		if fail != nil {
			return nil, fail
		}
		return &types.OrderDetails{ID: id, CreatedAt: time.Unix(1_800_000_000, 0), CatalogID: "c1",
			Price:    types.OrderPrice{Subtotal: 100000, Total: 119800, Currency: "BRL"},
			Products: []types.OrderProduct{{ID: "77", Name: "Bolo", Price: 25000, Currency: "BRL", Quantity: 2}}}, nil
	}
	d, err := w.Order(ctx, "123", "tok")
	if err != nil || gotID != "123" || gotToken != "tok" || d.TotalAmount_1000 != 119800 || len(d.Items) != 1 ||
		d.Items[0].Quantity != 2 || d.CreatedMs != 1_800_000_000_000 {
		t.Fatalf("order = %+v, %v", d, err)
	}
	if _, err := w.Order(ctx, "", "tok"); err == nil {
		t.Fatal("an order without id must be refused")
	}
	fail = whatsmeow.ErrIQTimedOut
	if c, code := codeOf(errOf(w.Order(ctx, "123", "tok"))); c != codes.DeadlineExceeded || code != "worker_timeout" {
		t.Fatalf("timeout = %v %v", c, code)
	}
	fail = &whatsmeow.IQError{Code: 404, Text: "item-not-found"}
	if c, code := codeOf(errOf(w.Order(ctx, "123", "tok"))); c != codes.NotFound || code != "not_found" {
		t.Fatalf("404 = %v %v", c, code)
	}
	fail = errors.New("boom")
	if _, err := w.Order(ctx, "123", "tok"); err == nil {
		t.Fatal("a failure must surface")
	}
}

// jsonEqual compares two JSON documents (protojson's spacing is not stable).
func jsonEqual(t *testing.T, got []byte, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(a, b)
}
