package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/menu"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/go-chi/chi/v5"
)

// MenuService is the Cardápio (LiteZap's own catalog); service.Menu implements it.
type MenuService interface {
	Get(ctx context.Context, instanceID string) (service.MenuView, error)
	Item(ctx context.Context, instanceID, itemID string) (service.MenuItemView, error)
	Text(ctx context.Context, instanceID string, in service.MenuTextInput) (service.MenuTextView, error)
	SendMenu(ctx context.Context, instanceID string, in service.SendMenuInput, wait time.Duration) (service.SendMenuResult, error)

	CreateCategory(ctx context.Context, instanceID string, in service.MenuCategoryInput) (service.MenuCategoryView, error)
	RenameCategory(ctx context.Context, instanceID, catID string, in service.MenuCategoryInput) (service.MenuCategoryView, error)
	DeleteCategory(ctx context.Context, instanceID, catID string) error
	ReorderCategories(ctx context.Context, instanceID string, in service.ReorderInput) error

	CreateItem(ctx context.Context, instanceID string, in service.MenuItemInput) (service.MenuItemView, error)
	UpdateItem(ctx context.Context, instanceID, itemID string, in service.MenuItemInput) (service.MenuItemView, error)
	DeleteItem(ctx context.Context, instanceID, itemID string) error
	SetAvailable(ctx context.Context, instanceID, itemID string, available bool) (service.MenuItemView, error)
	ReorderItems(ctx context.Context, instanceID string, in service.ReorderInput) error

	AddOption(ctx context.Context, instanceID, itemID string, in menu.Option) (menu.Option, error)
	UpdateOption(ctx context.Context, instanceID, itemID, optID string, in menu.Option) (menu.Option, error)
	DeleteOption(ctx context.Context, instanceID, itemID, optID string) error

	SetImage(ctx context.Context, instanceID, itemID string, in service.MenuImageInput) (service.MenuItemView, error)
	DeleteImage(ctx context.Context, instanceID, itemID string) (service.MenuItemView, error)
	Image(ctx context.Context, instanceID, itemID string) ([]byte, error)

	ImportCSV(ctx context.Context, instanceID string, in service.MenuCSVInput) (service.MenuImportView, error)
	ImportRows(ctx context.Context, instanceID string, in service.MenuRowsInput) (service.MenuImportView, error)
	BuildWithAI(ctx context.Context, instanceID string, in service.MenuAIInput) (service.MenuImportView, error)
	ImportCatalog(ctx context.Context, instanceID string, in service.MenuCatalogInput) (service.MenuImportView, error)
}

// maxMenuImageBody fits an 8MB photo as base64 plus the JSON around it.
const maxMenuImageBody = 12 << 20

func (s *server) menuRoutes(r chi.Router) {
	if s.opt.Menu == nil {
		return
	}
	r.Get("/menu", s.getMenu)
	r.Get("/menu/text", s.menuText)
	r.Post("/menu/send", s.sendMenu)
	r.Post("/menu/categories", s.createMenuCategory)
	r.Put("/menu/categories/order", s.reorderMenuCategories)
	r.Patch("/menu/categories/{cat_id}", s.renameMenuCategory)
	r.Delete("/menu/categories/{cat_id}", s.deleteMenuCategory)
	r.Post("/menu/items", s.createMenuItem)
	r.Put("/menu/items/order", s.reorderMenuItems)
	r.Get("/menu/items/{item_id}", s.getMenuItem)
	r.Patch("/menu/items/{item_id}", s.updateMenuItem)
	r.Delete("/menu/items/{item_id}", s.deleteMenuItem)
	r.Put("/menu/items/{item_id}/available", s.setMenuItemAvailable)
	r.Get("/menu/items/{item_id}/image", s.menuItemImage)
	r.Put("/menu/items/{item_id}/image", s.setMenuItemImage)
	r.Delete("/menu/items/{item_id}/image", s.deleteMenuItemImage)
	r.Post("/menu/items/{item_id}/options", s.addMenuOption)
	r.Patch("/menu/items/{item_id}/options/{option_id}", s.updateMenuOption)
	r.Delete("/menu/items/{item_id}/options/{option_id}", s.deleteMenuOption)
	r.Post("/menu/items/{item_id}/send", s.sendMenuItem)
	r.Post("/menu/import/csv", s.importMenuCSV)
	r.Post("/menu/import/rows", s.importMenuRows)
	r.Post("/menu/import/ai", s.buildMenuWithAI)
	r.Post("/menu/import/whatsapp-catalog", s.importMenuCatalog)
}

// getMenu is the whole menu. include_empty=false leaves out the categories without items
// (default true: the management view lists them, to rename or delete them).
func (s *server) getMenu(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Menu.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if r.URL.Query().Get("include_empty") == "false" {
		kept := v.Categories[:0]
		for _, c := range v.Categories {
			if len(c.Items) > 0 {
				kept = append(kept, c)
			}
		}
		v.Categories = kept
	}
	writeJSON(w, 200, v)
}

func (s *server) menuText(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.opt.Menu.Text(r.Context(), chi.URLParam(r, "id"), service.MenuTextInput{CategoryID: q.Get("category_id"),
		IncludeUnavailable: q.Get("include_unavailable") == "true", Title: q.Get("title")})
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) sendMenu(w http.ResponseWriter, r *http.Request) {
	var in service.SendMenuInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.SendMenu(r.Context(), chi.URLParam(r, "id"), in, 0)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}

func (s *server) createMenuCategory(w http.ResponseWriter, r *http.Request) {
	var in service.MenuCategoryInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.CreateCategory(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) renameMenuCategory(w http.ResponseWriter, r *http.Request) {
	var in service.MenuCategoryInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.RenameCategory(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "cat_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) deleteMenuCategory(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Menu.DeleteCategory(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "cat_id")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) reorderMenuCategories(w http.ResponseWriter, r *http.Request) {
	var in service.ReorderInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	if err := s.opt.Menu.ReorderCategories(r.Context(), chi.URLParam(r, "id"), in); err != nil {
		s.writeErr(w, err)
		return
	}
	s.getMenu(w, r)
}

func (s *server) reorderMenuItems(w http.ResponseWriter, r *http.Request) {
	var in service.ReorderInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	if err := s.opt.Menu.ReorderItems(r.Context(), chi.URLParam(r, "id"), in); err != nil {
		s.writeErr(w, err)
		return
	}
	s.getMenu(w, r)
}

func (s *server) createMenuItem(w http.ResponseWriter, r *http.Request) {
	var in service.MenuItemInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.CreateItem(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) getMenuItem(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Menu.Item(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) updateMenuItem(w http.ResponseWriter, r *http.Request) {
	var in service.MenuItemInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.UpdateItem(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) deleteMenuItem(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Menu.DeleteItem(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) setMenuItemAvailable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Available *bool `json:"available"`
	}
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	if in.Available == nil {
		s.writeErr(w, apierr.Validation("available is required (true or false)"))
		return
	}
	v, err := s.opt.Menu.SetAvailable(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), *in.Available)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) menuItemImage(w http.ResponseWriter, r *http.Request) {
	b, err := s.opt.Menu.Image(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Cache-Control", "private, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *server) setMenuItemImage(w http.ResponseWriter, r *http.Request) {
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
	v, err := s.opt.Menu.SetImage(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) deleteMenuItemImage(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Menu.DeleteImage(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) addMenuOption(w http.ResponseWriter, r *http.Request) {
	var in menu.Option
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.AddOption(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) updateMenuOption(w http.ResponseWriter, r *http.Request) {
	var in menu.Option
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Menu.UpdateOption(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), chi.URLParam(r, "option_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) deleteMenuOption(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Menu.DeleteOption(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), chi.URLParam(r, "option_id")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sendMenuItem is POST /messages with type menu_item for this item.
func (s *server) sendMenuItem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		To              string `json:"to"`
		Caption         string `json:"caption,omitempty"`
		ReplyTo         string `json:"reply_to,omitempty"`
		ClientMessageID string `json:"client_message_id,omitempty"`
		ExpiresAfter    string `json:"expires_after,omitempty"`
	}
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.msg.Send(r.Context(), chi.URLParam(r, "id"), service.SendInput{To: in.To, Type: "menu_item",
		MenuItemID: chi.URLParam(r, "item_id"), Caption: in.Caption, ReplyTo: in.ReplyTo, ClientMessageID: in.ClientMessageID,
		ExpiresAfter: in.ExpiresAfter}, 0)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	code := http.StatusAccepted
	if v.Status == "sent" {
		code = http.StatusOK
	}
	writeJSON(w, code, v)
}

func (s *server) importMenuCSV(w http.ResponseWriter, r *http.Request) {
	var in service.MenuCSVInput
	if err := decodeMax(w, r, &in, 2*menu.MaxCSVBytes); err != nil {
		s.writeErr(w, err)
		return
	}
	s.menuImportResult(w, in.DryRun)(s.opt.Menu.ImportCSV(r.Context(), chi.URLParam(r, "id"), in))
}

func (s *server) importMenuRows(w http.ResponseWriter, r *http.Request) {
	var in service.MenuRowsInput
	if err := decodeMax(w, r, &in, 4<<20); err != nil {
		s.writeErr(w, err)
		return
	}
	s.menuImportResult(w, in.DryRun)(s.opt.Menu.ImportRows(r.Context(), chi.URLParam(r, "id"), in))
}

func (s *server) buildMenuWithAI(w http.ResponseWriter, r *http.Request) {
	var in service.MenuAIInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	s.menuImportResult(w, true)(s.opt.Menu.BuildWithAI(r.Context(), chi.URLParam(r, "id"), in))
}

func (s *server) importMenuCatalog(w http.ResponseWriter, r *http.Request) {
	var in service.MenuCatalogInput
	if r.ContentLength != 0 {
		if err := decode(w, r, &in); err != nil {
			s.writeErr(w, err)
			return
		}
	}
	s.menuImportResult(w, in.DryRun)(s.opt.Menu.ImportCatalog(r.Context(), chi.URLParam(r, "id"), in))
}

// menuImportResult writes an import's outcome: 200 for a preview, 201 for a commit.
// A refused commit (rows with errors) still returns its rows, inside the error body.
func (s *server) menuImportResult(w http.ResponseWriter, dry bool) func(service.MenuImportView, error) {
	return func(v service.MenuImportView, err error) {
		if err != nil {
			if ae, ok := err.(*apierr.Error); ok && len(v.Rows) > 0 && ae.Status == http.StatusBadRequest {
				writeJSON(w, ae.Status, map[string]any{"error": ae, "preview": v})
				return
			}
			s.writeErr(w, err)
			return
		}
		code := http.StatusCreated
		if dry {
			code = http.StatusOK
		}
		writeJSON(w, code, v)
	}
}
