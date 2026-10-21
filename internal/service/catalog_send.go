package service

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/store"
)

const (
	maxProductBody   = 1024
	maxProductFooter = 60
)

// composeProduct builds a type product send (marco 12, research §5): it reads the product
// from the catalog (the instance's own unless catalog_owner), downloads its first image
// through the instance's proxy and stages it; the worker uploads it as a normal
// encrypted image with this JPEG thumbnail and builds the ProductMessage. It goes through
// the send queue like any message.
func (s *Messaging) composeProduct(ctx context.Context, instanceID, id string, in SendInput, p *queue.Payload) error {
	r := s.Catalog
	if r == nil || r.Client == nil {
		return apierr.New(http.StatusNotImplemented, "not_implemented", "product messages need the WhatsApp catalog, not available on this server", false)
	}
	if !validProductID(in.ProductID) {
		return apierr.Validation("product_id is required: the numeric id of a catalog product (GET /catalog)")
	}
	if in.Text != "" || in.Caption != "" || in.Media != nil {
		return apierr.Validation("type product takes body and footer (not text, caption or media)")
	}
	if utf8.RuneCountInString(in.Body) > maxProductBody || utf8.RuneCountInString(in.Footer) > maxProductFooter {
		return apierr.Validation("body is limited to 1024 and footer to 60 characters")
	}
	inst, err := s.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return mapErr(err)
	}
	owner, _, err := catalogOwner(inst, in.CatalogOwner)
	if err != nil {
		return err
	}
	prod, err := r.rawProduct(ctx, instanceID, owner, in.ProductID)
	if err != nil {
		return err
	}
	if prod.Hidden {
		return apierr.Validation("this product is hidden in the catalog: customers could not open it")
	}
	if prod.ReviewStatus != "" && prod.ReviewStatus != "approved" {
		return apierr.Validation("this product is " + prod.ReviewStatus + " in WhatsApp's review: customers cannot open it yet")
	}
	if len(prod.Images) == 0 {
		return apierr.Validation("this product has no image: WhatsApp shows product cards with their image")
	}
	release, err := s.acquireMedia(ctx)
	if err != nil {
		return err
	}
	defer release()
	data, mime, err := r.image(ctx, instanceID, inst, prod)
	if err != nil {
		return err
	}
	if mime != "image/jpeg" && mime != "image/png" {
		return apierr.Validation("the product image is not a JPEG or PNG")
	}
	dir := filepath.Join(s.DataDir, "outbox", instanceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file := filepath.Join(dir, id)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return err
	}
	body, footer := strings.TrimSpace(in.Body), strings.TrimSpace(in.Footer)
	p.MediaFile, p.Mime, p.Size = file, mime, int64(len(data))
	p.Text = body
	if p.Text == "" {
		p.Text = prod.Name
	}
	p.Product = &queue.Product{ProductID: prod.ID, Title: prod.Name, Description: prod.Description, Currency: prod.Currency,
		PriceAmount1000: prod.PriceAmount1000, SaleAmount1000: prod.SaleAmount1000, RetailerID: prod.RetailerID, URL: prod.URL,
		ImageCount: uint32(len(prod.Images)), BusinessOwnerJID: owner, Body: body, Footer: footer, Thumbnail: thumbnail(data)}
	if prod.Currency == "" {
		p.Product.PriceAmount1000, p.Product.SaleAmount1000 = 0, 0
	}
	metaOf(p).Product = &store.ProductCard{ProductID: prod.ID, Title: prod.Name, Description: prod.Description,
		PriceAmount1000: prod.PriceAmount1000, Currency: prod.Currency, RetailerID: prod.RetailerID, URL: prod.URL,
		BusinessOwner: owner, Body: body, Footer: footer}
	return nil
}
