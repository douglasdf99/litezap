package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/catalog"
	"github.com/douglasdf99/litezap/internal/store"
)

// ---- catalog and orders through Social (the API and MCP surface, marco 12) ----

func (s *Social) catalog() *CatalogReader {
	if s.Messaging == nil {
		return nil
	}
	return s.Messaging.Catalog
}

// Catalog reads a page of a WhatsApp catalog (jid "": the instance's own).
func (s *Social) Catalog(ctx context.Context, instanceID, jid string, limit int, cursor string) (CatalogView, error) {
	return s.catalog().Page(ctx, instanceID, jid, limit, cursor)
}

// CatalogProduct reads one product.
func (s *Social) CatalogProduct(ctx context.Context, instanceID, jid, productID string) (CatalogProductView, error) {
	return s.catalog().Product(ctx, instanceID, jid, productID)
}

// CatalogCollections lists a catalog's collections.
func (s *Social) CatalogCollections(ctx context.Context, instanceID, jid string) (CollectionsView, error) {
	return s.catalog().Collections(ctx, instanceID, jid)
}

// CatalogProductImage downloads a product's first image.
func (s *Social) CatalogProductImage(ctx context.Context, instanceID, jid, productID string) ([]byte, string, error) {
	return s.catalog().ProductImage(ctx, instanceID, jid, productID)
}

// OrderView is an order received from the catalog (the token is never shown). Amounts
// are in thousandths of the currency unit; total is the same in units.
type OrderView struct {
	OrderID         string    `json:"order_id"`
	MessageID       string    `json:"message_id"`
	Chat            string    `json:"chat"`
	Seller          string    `json:"seller,omitempty"`
	ItemCount       int       `json:"item_count"`
	TotalAmount1000 int64     `json:"total_amount_1000"`
	Total           float64   `json:"total"`
	Currency        string    `json:"currency,omitempty"`
	TotalText       string    `json:"total_text,omitempty"`
	Title           string    `json:"title,omitempty"`
	Note            string    `json:"note,omitempty"`
	Status          string    `json:"status,omitempty"`
	ReceivedAt      time.Time `json:"received_at"`
}

// OrderItemView is one line of an order.
type OrderItemView struct {
	ProductID       string  `json:"product_id"`
	Name            string  `json:"name"`
	Quantity        int     `json:"quantity"`
	PriceAmount1000 int64   `json:"price_amount_1000"`
	Price           float64 `json:"price"`
	Currency        string  `json:"currency,omitempty"`
	ImageURL        string  `json:"image_url,omitempty"`
	Variant         string  `json:"variant,omitempty"`
}

// OrderDetailsView is an order with its items, as WhatsApp answers it.
type OrderDetailsView struct {
	OrderView
	CreatedAt          *time.Time      `json:"created_at,omitempty"`
	SubtotalAmount1000 int64           `json:"subtotal_amount_1000"`
	Items              []OrderItemView `json:"items"`
	// Source says how the details were read: socket (whatsmeow GetOrderDetails) or graphql
	// (the public catalog GraphQL fallback).
	Source string `json:"source"`
}

func orderView(o store.Order) OrderView {
	v := OrderView{OrderID: o.OrderID, MessageID: o.MessageID, Chat: o.ChatJID, Seller: o.SellerJID, ItemCount: o.ItemCount,
		TotalAmount1000: o.Total1000, Total: float64(o.Total1000) / 1000, Currency: o.Currency, Title: o.Title, Note: o.Note,
		Status: o.Status, ReceivedAt: o.ReceivedAt}
	if o.Currency == "BRL" && o.Total1000 > 0 {
		v.TotalText = catalog.FormatBRL(o.Total1000)
	}
	return v
}

// Orders lists the newest orders received (limit 1-100, default 20).
func (s *Social) Orders(ctx context.Context, instanceID string, limit int) ([]OrderView, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return nil, mapErr(err)
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		return nil, apierr.Validation("limit is 1 to 100")
	}
	list, err := s.Store.RecentOrders(ctx, instanceID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]OrderView, 0, len(list))
	for _, o := range list {
		out = append(out, orderView(o))
	}
	return out, nil
}

// Order reads an order's details: whatsmeow's GetOrderDetails first (the socket IQ), then
// the public catalog GraphQL when the IQ fails (WA Web moved to GraphQL; whether the IQ
// still answers is unverified). The token comes from the stored order message.
func (s *Social) Order(ctx context.Context, instanceID, orderID string) (OrderDetailsView, error) {
	orderID = strings.TrimSpace(orderID)
	if orderID == "" || len(orderID) > 64 {
		return OrderDetailsView{}, apierr.Validation("order_id is required")
	}
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return OrderDetailsView{}, mapErr(err)
	}
	o, err := s.Store.GetOrder(ctx, instanceID, orderID)
	if errors.Is(err, store.ErrNotFound) {
		return OrderDetailsView{}, apierr.New(http.StatusNotFound, "order_not_found",
			"no order with this id was received by this number (orders are kept from their order message)", false)
	}
	if err != nil {
		return OrderDetailsView{}, err
	}
	out := OrderDetailsView{OrderView: orderView(o), Items: []OrderItemView{}}
	sockErr := s.orderSocket(ctx, instanceID, o, &out)
	if sockErr == nil {
		return out, nil
	}
	r := s.catalog()
	if r == nil || r.Client == nil {
		return OrderDetailsView{}, socialErr(sockErr)
	}
	seller := o.SellerJID
	if seller == "" {
		in, _ := s.Store.GetInstance(ctx, instanceID)
		seller, _, _ = catalogOwner(in, "")
	}
	d, err := r.orderGraphQL(ctx, instanceID, seller, o.OrderID, o.Token)
	if err != nil {
		return OrderDetailsView{}, catalogErr(err, "order")
	}
	out.Source, out.SubtotalAmount1000 = "graphql", d.SubtotalAmount1000
	if d.TotalAmount1000 > 0 {
		out.TotalAmount1000, out.Total = d.TotalAmount1000, float64(d.TotalAmount1000)/1000
	}
	if d.Currency != "" {
		out.Currency = d.Currency
	}
	if !d.CreatedAt.IsZero() {
		t := d.CreatedAt
		out.CreatedAt = &t
	}
	for _, it := range d.Items {
		out.Items = append(out.Items, OrderItemView{ProductID: it.ProductID, Name: it.Name, Quantity: it.Quantity,
			PriceAmount1000: it.PriceAmount1000, Price: float64(it.PriceAmount1000) / 1000, Currency: it.Currency, ImageURL: it.ImageURL})
	}
	return out, nil
}

// orderSocket reads the order through the worker (nil error: out is filled).
func (s *Social) orderSocket(ctx context.Context, instanceID string, o store.Order, out *OrderDetailsView) error {
	if s.Workers == nil {
		return errors.New("no workers")
	}
	cli, err := s.Workers.Social(instanceID)
	if err != nil {
		return err
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	d, err := cli.GetOrder(c, &workerv1.GetOrderRequest{OrderId: o.OrderID, Token: o.Token})
	if err != nil {
		return err // any failure (timeout, not connected, refused): the GraphQL fallback
	}
	out.Source, out.SubtotalAmount1000 = "socket", d.SubtotalAmount_1000
	if d.TotalAmount_1000 > 0 {
		out.TotalAmount1000, out.Total = d.TotalAmount_1000, float64(d.TotalAmount_1000)/1000
	}
	if d.Currency != "" {
		out.Currency = d.Currency
	}
	if d.CreatedMs > 0 {
		t := time.UnixMilli(d.CreatedMs).UTC()
		out.CreatedAt = &t
	}
	for _, it := range d.Items {
		out.Items = append(out.Items, OrderItemView{ProductID: it.ProductId, Name: it.Name, Quantity: int(it.Quantity),
			PriceAmount1000: it.PriceAmount_1000, Price: float64(it.PriceAmount_1000) / 1000, Currency: it.Currency,
			ImageURL: it.ImageUrl, Variant: it.Variant})
	}
	return nil
}
