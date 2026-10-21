package worker

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// Catalog phase 1 (marco 12; research in docs/superpowers/specs/2026-10-21-catalog-protocol-
// research.md): the worker only sends product messages, parses received product and order
// messages and reads order details over the socket. Catalog reads are HTTPS GraphQL, done
// by the supervisor through the instance's proxy.

// OrderClient is implemented by WAClients that read order details. Optional: without it
// GetOrder answers Unimplemented and the supervisor uses its GraphQL fallback.
type OrderClient interface {
	Order(ctx context.Context, orderID, token string) (*workerv1.OrderDetails, error)
}

var _ OrderClient = (*Whatsmeow)(nil)

// orderTimeout bounds the order IQ: WA Web moved to GraphQL and an unanswered legacy IQ
// must not wait out the default IQ timeout (research §7).
const orderTimeout = 10 * time.Second

func (g *SocialServer) GetOrder(ctx context.Context, req *workerv1.GetOrderRequest) (*workerv1.OrderDetails, error) {
	c, ok := g.s.wa.(OrderClient)
	if !ok {
		return nil, grpcErr(codes.Unimplemented, "not_supported", "orders not supported by this worker")
	}
	return c.Order(ctx, req.OrderId, req.Token)
}

// Order reads an order with whatsmeow's GetOrderDetails (fb:thrift_iq, smax 5).
func (w *Whatsmeow) Order(ctx context.Context, orderID, token string) (*workerv1.OrderDetails, error) {
	if err := w.loggedIn(); err != nil {
		return nil, err
	}
	if orderID == "" || token == "" || len(orderID) > 64 || len(token) > 4096 {
		return nil, invalid("order id and token are required")
	}
	get := w.orderDetails
	if get == nil {
		get = w.cli.GetOrderDetails
	}
	c, cancel := context.WithTimeout(ctx, orderTimeout)
	defer cancel()
	d, err := get(c, orderID, token)
	if err != nil {
		var iq *whatsmeow.IQError
		switch {
		case errors.As(err, &iq) && iq.Code == 404:
			return nil, grpcErr(codes.NotFound, "not_found", "order not found")
		case errors.Is(err, whatsmeow.ErrIQTimedOut), errors.Is(err, context.DeadlineExceeded):
			return nil, grpcErr(codes.DeadlineExceeded, "worker_timeout", "WhatsApp did not answer the order query")
		}
		return nil, socialErr(err)
	}
	return orderDetails(d), nil
}

func orderDetails(d *types.OrderDetails) *workerv1.OrderDetails {
	out := &workerv1.OrderDetails{Id: d.ID, CatalogId: d.CatalogID, SubtotalAmount_1000: d.Price.Subtotal,
		TotalAmount_1000: d.Price.Total, Currency: d.Price.Currency, PriceStatus: d.Price.PriceStatus}
	if !d.CreatedAt.IsZero() {
		out.CreatedMs = d.CreatedAt.UnixMilli()
	}
	for _, p := range d.Products {
		out.Items = append(out.Items, &workerv1.OrderItem{ProductId: p.ID, Name: p.Name, PriceAmount_1000: p.Price,
			Currency: p.Currency, Quantity: int32(p.Quantity), ImageUrl: p.ImageURL, Variant: p.VariantInfo.Properties})
	}
	return out
}

// buildProductMessage builds a ProductMessage the way WA Web does
// (WAWebProductCatalogMessage.createProductMsgSnapshot): the product's head image as a
// normal encrypted image with a JPEG thumbnail, the product id, title, description, price
// (omitted when 0), image count and the owner's phone JID; no CatalogSnapshot.
func buildProductMessage(p *workerv1.Product, mime string, up whatsmeow.UploadResponse) *waE2E.Message {
	snap := &waE2E.ProductMessage_ProductSnapshot{
		ProductImage: &waE2E.ImageMessage{
			Mimetype: proto.String(mime), URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength), JPEGThumbnail: p.Thumbnail,
		},
		ProductID: proto.String(p.ProductId),
		Title:     proto.String(p.Title),
	}
	if p.Description != "" {
		snap.Description = proto.String(p.Description)
	}
	if p.PriceAmount_1000 > 0 && p.Currency != "" {
		snap.CurrencyCode, snap.PriceAmount1000 = proto.String(p.Currency), proto.Int64(p.PriceAmount_1000)
		if p.SalePriceAmount_1000 > 0 && p.SalePriceAmount_1000 < p.PriceAmount_1000 {
			snap.SalePriceAmount1000 = proto.Int64(p.SalePriceAmount_1000)
		}
	}
	if p.RetailerId != "" {
		snap.RetailerID = proto.String(p.RetailerId)
	}
	if p.Url != "" {
		snap.URL = proto.String(p.Url)
	}
	if p.ImageCount > 0 {
		snap.ProductImageCount = proto.Uint32(p.ImageCount)
	}
	pm := &waE2E.ProductMessage{Product: snap, BusinessOwnerJID: proto.String(p.BusinessOwnerJid)}
	if p.Body != "" {
		pm.Body = proto.String(p.Body)
	}
	if p.Footer != "" {
		pm.Footer = proto.String(p.Footer)
	}
	return &waE2E.Message{ProductMessage: pm}
}

// buildProduct uploads the staged product image and builds the product message.
func (w *Whatsmeow) buildProduct(ctx context.Context, req *workerv1.SendRequest) (*waE2E.Message, error) {
	p := req.Product
	if p == nil || p.ProductId == "" || p.Title == "" {
		return nil, invalid("product needs a product id and a title")
	}
	owner, err := types.ParseJID(p.BusinessOwnerJid)
	if err != nil || owner.User == "" || owner.Server != types.DefaultUserServer {
		return nil, invalid("product needs the owner's phone JID")
	}
	path, err := w.dataPath(req.MediaPath, "outbox")
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, invalid("product image is missing")
	}
	up, err := w.uploadFile(ctx, f, whatsmeow.MediaImage)
	f.Close()
	if err != nil {
		return nil, grpcErr(codes.Unavailable, "media_upload_failed", err.Error())
	}
	mime := req.Mime
	if mime == "" {
		mime = "image/jpeg"
	}
	return buildProductMessage(p, mime, up), nil
}

// productOf converts a received ProductMessage (nil when m has none).
func productOf(m *waE2E.Message) *workerv1.Product {
	pm := m.GetProductMessage()
	if pm == nil {
		return nil
	}
	s := pm.GetProduct()
	return &workerv1.Product{ProductId: s.GetProductID(), Title: s.GetTitle(), Description: s.GetDescription(),
		Currency: s.GetCurrencyCode(), PriceAmount_1000: s.GetPriceAmount1000(), SalePriceAmount_1000: s.GetSalePriceAmount1000(),
		RetailerId: s.GetRetailerID(), Url: s.GetURL(), ImageCount: s.GetProductImageCount(),
		BusinessOwnerJid: pm.GetBusinessOwnerJID(), Body: pm.GetBody(), Footer: pm.GetFooter()}
}

// orderOf converts a received OrderMessage (nil when m has none). The token is kept: the
// supervisor stores it to read the order details and never exposes it.
func orderOf(m *waE2E.Message) *workerv1.Order {
	o := m.GetOrderMessage()
	if o == nil {
		return nil
	}
	st := ""
	switch o.GetStatus() {
	case waE2E.OrderMessage_INQUIRY:
		st = "inquiry"
	case waE2E.OrderMessage_ACCEPTED:
		st = "accepted"
	case waE2E.OrderMessage_DECLINED:
		st = "declined"
	}
	seller := o.GetSellerJID()
	if j, err := types.ParseJID(seller); err == nil && j.User != "" {
		seller = j.ToNonAD().String()
	}
	return &workerv1.Order{OrderId: o.GetOrderID(), Token: o.GetToken(), SellerJid: seller, ItemCount: o.GetItemCount(),
		Title: o.GetOrderTitle(), Message: o.GetMessage(), TotalAmount_1000: o.GetTotalAmount1000(),
		Currency: o.GetTotalCurrencyCode(), Status: st}
}

// productText is the logged text of a product message: its caption, else its title.
func productText(m *waE2E.Message) string {
	pm := m.GetProductMessage()
	if b := strings.TrimSpace(pm.GetBody()); b != "" {
		return b
	}
	return pm.GetProduct().GetTitle()
}

// orderText is the logged text of an order: the buyer's note, else the order title.
func orderText(m *waE2E.Message) string {
	o := m.GetOrderMessage()
	if t := strings.TrimSpace(o.GetMessage()); t != "" {
		return t
	}
	return o.GetOrderTitle()
}
