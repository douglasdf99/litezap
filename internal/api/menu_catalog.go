package api

import (
	"net/http"
	"strconv"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/go-chi/chi/v5"
)

// Cardápio routes for the WhatsApp-catalog parity: search, hidden items, duplicates,
// bulk changes, several photos, collections, the CSV export and the order drafts.
// Nothing here writes to Meta or WhatsApp; the sends go through the queue.

func (s *server) menuCatalogRoutes(r chi.Router) {
	r.Get("/menu/export.csv", s.exportMenuCSV)
	r.Get("/menu/items", s.searchMenu)
	r.Post("/menu/items/bulk", s.bulkMenuItems)
	r.Put("/menu/items/{item_id}/hidden", s.setMenuItemHidden)
	r.Post("/menu/items/{item_id}/duplicate", s.duplicateMenuItem)
	r.Post("/menu/items/{item_id}/images", s.addMenuItemImage)
	r.Put("/menu/items/{item_id}/images/order", s.reorderMenuItemImages)
	r.Get("/menu/items/{item_id}/images/{image_id}", s.menuItemImageByID)
	r.Delete("/menu/items/{item_id}/images/{image_id}", s.removeMenuItemImage)
	r.Put("/menu/items/{item_id}/images/{image_id}/main", s.setMenuItemMainImage)

	r.Get("/menu/collections", s.listMenuCollections)
	r.Post("/menu/collections", s.createMenuCollection)
	r.Put("/menu/collections/order", s.reorderMenuCollections)
	r.Get("/menu/collections/{col_id}", s.getMenuCollection)
	r.Patch("/menu/collections/{col_id}", s.updateMenuCollection)
	r.Delete("/menu/collections/{col_id}", s.deleteMenuCollection)
	r.Post("/menu/collections/{col_id}/items", s.addMenuCollectionItems)
	r.Post("/menu/collections/{col_id}/items/remove", s.removeMenuCollectionItems)
	r.Put("/menu/collections/{col_id}/items/order", s.reorderMenuCollectionItems)
	r.Get("/menu/collections/{col_id}/text", s.menuCollectionText)
	r.Post("/menu/collections/{col_id}/send", s.sendMenuCollection)

	r.Get("/menu/orders", s.listOrderDrafts)
	r.Post("/menu/orders", s.createOrderDraft)
	r.Get("/menu/orders/{order_id}", s.getOrderDraft)
	r.Patch("/menu/orders/{order_id}", s.setOrderDraftStatus)
	r.Post("/menu/orders/{order_id}/send", s.sendOrderSummary)
}

// boolQuery reads an optional true/false query parameter.
func boolQuery(r *http.Request, name string) (*bool, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil, apierr.Validation(name + " is true or false")
	}
	return &b, nil
}

func intQuery(r *http.Request, name string) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, apierr.Validation(name + " is a number")
	}
	return n, nil
}

func (s *server) exportMenuCSV(w http.ResponseWriter, r *http.Request) {
	b, err := s.opt.Menu.ExportCSV(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="cardapio.csv"`)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// searchMenu is GET /menu/items: q, tag, collection_id, category_id ("" = no
// category, when present), available, hidden, on_sale, limit and offset.
func (s *server) searchMenu(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := service.MenuSearchInput{Query: q.Get("q"), Tag: q.Get("tag"), CollectionID: q.Get("collection_id")}
	if q.Has("category_id") {
		c := q.Get("category_id")
		in.CategoryID = &c
	}
	var err error
	for _, f := range []struct {
		name string
		dst  **bool
	}{{"available", &in.Available}, {"hidden", &in.Hidden}, {"on_sale", &in.OnSale}} {
		if *f.dst, err = boolQuery(r, f.name); err != nil {
			s.writeErr(w, err)
			return
		}
	}
	if in.Limit, err = intQuery(r, "limit"); err != nil {
		s.writeErr(w, err)
		return
	}
	if in.Offset, err = intQuery(r, "offset"); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.Search(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) bulkMenuItems(w http.ResponseWriter, r *http.Request) {
	var in service.MenuBulkInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.Bulk(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		if ae, ok := err.(*apierr.Error); ok && ae.Status == http.StatusBadRequest && len(v.Changes) > 0 {
			writeJSON(w, ae.Status, map[string]any{"error": ae, "preview": v})
			return
		}
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) setMenuItemHidden(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Hidden *bool `json:"hidden"`
	}
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	if in.Hidden == nil {
		s.writeErr(w, apierr.Validation("hidden is required (true or false)"))
		return
	}
	v, err := s.opt.Menu.SetHidden(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), *in.Hidden)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) duplicateMenuItem(w http.ResponseWriter, r *http.Request) {
	var in service.MenuDuplicateInput
	if r.ContentLength != 0 {
		if err := decode(w, r, &in); err != nil {
			s.writeErr(w, err)
			return
		}
	}
	v, err := s.opt.Menu.DuplicateItem(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) addMenuItemImage(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > bigBody || r.ContentLength < 0 {
		select { // large bodies are held in memory while decoded: a few at a time
		case s.bodySlots <- struct{}{}:
			defer func() { <-s.bodySlots }()
		case <-r.Context().Done():
			s.writeErr(w, apierr.New(http.StatusServiceUnavailable, "rate_limited", "too many large uploads in progress", true))
			return
		}
	}
	var in service.MenuImageInput
	if err := decodeMax(w, r, &in, maxMenuImageBody); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.AddImage(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) reorderMenuItemImages(w http.ResponseWriter, r *http.Request) {
	var in service.ReorderInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.ReorderImages(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) menuItemImageByID(w http.ResponseWriter, r *http.Request) {
	b, err := s.opt.Menu.ImageByID(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), chi.URLParam(r, "image_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Cache-Control", "private, max-age=86400") // a photo id never changes content
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *server) removeMenuItemImage(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Menu.RemoveImage(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), chi.URLParam(r, "image_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) setMenuItemMainImage(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Menu.SetMainImage(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), chi.URLParam(r, "image_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

// ---- collections ----

func (s *server) listMenuCollections(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Menu.ListCollections(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"collections": v})
}

func (s *server) createMenuCollection(w http.ResponseWriter, r *http.Request) {
	var in service.MenuCollectionInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.CreateCollection(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) reorderMenuCollections(w http.ResponseWriter, r *http.Request) {
	var in service.ReorderInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	if err := s.opt.Menu.ReorderCollections(r.Context(), chi.URLParam(r, "id"), in); err != nil {
		s.writeErr(w, err)
		return
	}
	s.listMenuCollections(w, r)
}

func (s *server) getMenuCollection(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Menu.Collection(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "col_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) updateMenuCollection(w http.ResponseWriter, r *http.Request) {
	var in service.MenuCollectionInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.UpdateCollection(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "col_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) deleteMenuCollection(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Menu.DeleteCollection(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "col_id")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) addMenuCollectionItems(w http.ResponseWriter, r *http.Request) {
	var in service.MenuItemIDsInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.AddCollectionItems(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "col_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) removeMenuCollectionItems(w http.ResponseWriter, r *http.Request) {
	var in service.MenuItemIDsInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.RemoveCollectionItems(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "col_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) reorderMenuCollectionItems(w http.ResponseWriter, r *http.Request) {
	var in service.ReorderInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.ReorderCollectionItems(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "col_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) menuCollectionText(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.opt.Menu.Text(r.Context(), chi.URLParam(r, "id"), service.MenuTextInput{CollectionID: chi.URLParam(r, "col_id"),
		IncludeUnavailable: q.Get("include_unavailable") == "true", Title: q.Get("title")})
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) sendMenuCollection(w http.ResponseWriter, r *http.Request) {
	var in service.SendCollectionInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.SendCollection(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "col_id"), in, 0)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}

// ---- order drafts ----

func (s *server) listOrderDrafts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := intQuery(r, "limit")
	if err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.ListOrderDrafts(r.Context(), chi.URLParam(r, "id"), service.OrderDraftQuery{Status: q.Get("status"),
		Chat: q.Get("chat"), Source: q.Get("source"), Limit: limit})
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

// createOrderDraft prices and saves a cart (200 with dry_run, 201 saved). A cart with
// problems is refused with its preview in the error body.
func (s *server) createOrderDraft(w http.ResponseWriter, r *http.Request) {
	var in service.OrderDraftInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	if in.Source == "" {
		in.Source = service.OrderSourceAPI
	}
	v, err := s.opt.Menu.CreateOrderDraft(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		if ae, ok := err.(*apierr.Error); ok && ae.Status == http.StatusBadRequest && len(v.Items) > 0 {
			writeJSON(w, ae.Status, map[string]any{"error": ae, "preview": v})
			return
		}
		s.writeErr(w, err)
		return
	}
	code := http.StatusCreated
	if in.DryRun {
		code = http.StatusOK
	}
	writeJSON(w, code, v)
}

func (s *server) getOrderDraft(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Menu.OrderDraft(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "order_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) setOrderDraftStatus(w http.ResponseWriter, r *http.Request) {
	var in service.OrderStatusInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.SetOrderStatus(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "order_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) sendOrderSummary(w http.ResponseWriter, r *http.Request) {
	var in service.SendOrderInput
	if r.ContentLength != 0 {
		if err := decode(w, r, &in); err != nil {
			s.writeErr(w, err)
			return
		}
	}
	v, err := s.opt.Menu.SendOrderSummary(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "order_id"), in, 0)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}
