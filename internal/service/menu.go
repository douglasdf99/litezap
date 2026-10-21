package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/menu"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/store"
)

// ---- Cardápio: LiteZap's own catalog ----
//
// The menu lives in LiteZap (SQLite and data/menu/<instance>/), per instance. LiteZap never
// writes it to Meta or WhatsApp: it is sent as ordinary messages (a photo with a caption,
// or text) through the send queue, read by the attendant and the agents, and can be
// built by hand, from a spreadsheet, from the WhatsApp catalog (read only) or with the
// configured AI provider (review before saving).

// MenuSender queues messages (GuardedMessaging: the send suspension applies).
type MenuSender interface {
	Send(ctx context.Context, instanceID string, in SendInput, wait time.Duration) (MessageView, error)
}

// MenuAI runs one structured call with the instance's configured AI provider and
// returns the raw answer (attendant.Extract).
type MenuAI func(ctx context.Context, instanceID, system, user string, schema json.RawMessage) (string, error)

// Menu holds the menu use cases.
type Menu struct {
	Store   *store.Store
	DataDir string
	// Fetcher downloads image_url (the media.url rules: no private addresses unless
	// media_allow_private_urls, size limit).
	Fetcher *media.Fetcher
	// Catalog reads the WhatsApp catalog for the one-click copy (nil: not available).
	Catalog *CatalogReader
	Sender  MenuSender
	AI      MenuAI // nil: "Montar com IA" is not available

	imgOnce  sync.Once
	imgSlots chan struct{}
	aiQuota  windowQuota
}

// Per-instance quota of "Montar com IA" calls (each is a paid provider call).
var menuAIQuota = []window{{time.Minute, 5}, {24 * time.Hour, 100}}

const (
	maxImportImages = 30 // image downloads per import call (the rest: run it again)
	importImageTime = 90 * time.Second
	maxMenuBody     = 12 << 20 // image uploads as base64 JSON
	defaultMenuMsgs = 10
)

var menuIDRe = regexp.MustCompile(`^(mcat|mitm|mopt|mimg)_[0-9a-f]{16}$`)

func validMenuID(prefix, id string) bool {
	return strings.HasPrefix(id, prefix+"_") && menuIDRe.MatchString(id)
}

func (m *Menu) acquireImage(ctx context.Context) (func(), error) {
	m.imgOnce.Do(func() { m.imgSlots = make(chan struct{}, 2) })
	select {
	case m.imgSlots <- struct{}{}:
		return func() { <-m.imgSlots }, nil
	case <-ctx.Done():
		return nil, apierr.New(http.StatusServiceUnavailable, "rate_limited", "too many image uploads in progress", true)
	}
}

func menuErr(err error, what string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return notFound(what)
	case errors.Is(err, store.ErrMenuLimit):
		return apierr.New(http.StatusConflict, "menu_limit", fmt.Sprintf("the menu is limited to %d items, %d categories, %d option groups per item and %d choices per group",
			store.MaxMenuItems, store.MaxMenuCategories, store.MaxMenuOptions, store.MaxMenuChoices), false)
	}
	return err
}

func (m *Menu) instance(ctx context.Context, instanceID string) (store.Instance, error) {
	in, err := m.Store.GetInstance(ctx, instanceID)
	return in, mapErr(err)
}

// ---- views ----

// MenuItemView is an item as the API shows it.
type MenuItemView struct {
	ID          string        `json:"id"`
	CategoryID  string        `json:"category_id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	PriceCents  int64         `json:"price_cents"`
	PriceText   string        `json:"price_text"`
	Currency    string        `json:"currency"`
	Available   bool          `json:"available"`
	Position    int           `json:"position"`
	SKU         string        `json:"sku,omitempty"`
	Tags        []string      `json:"tags"`
	Options     []menu.Option `json:"options"`
	// ImagePath downloads the item's photo (JPEG) with the instance token.
	ImagePath string    `json:"image_path,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MenuCategoryView is a category with its items.
type MenuCategoryView struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Position int            `json:"position"`
	Items    []MenuItemView `json:"items"`
}

// MenuLimits are the per-instance limits.
type MenuLimits struct {
	MaxItems      int `json:"max_items"`
	MaxCategories int `json:"max_categories"`
	MaxOptions    int `json:"max_option_groups"`
	MaxChoices    int `json:"max_choices"`
}

// MenuView is the whole menu.
type MenuView struct {
	Categories []MenuCategoryView `json:"categories"`
	// Uncategorized are the items without category (shown last, as "Outros").
	Uncategorized []MenuItemView `json:"uncategorized"`
	ItemCount     int            `json:"item_count"`
	Limits        MenuLimits     `json:"limits"`
}

func itemView(instanceID string, it store.MenuItem) MenuItemView {
	v := MenuItemView{ID: it.ID, CategoryID: it.CategoryID, Name: it.Name, Description: it.Description, PriceCents: it.PriceCents,
		PriceText: menu.FormatPrice(it.PriceCents, it.Currency), Currency: it.Currency, Available: it.Available, Position: it.Position,
		SKU: it.SKU, Tags: it.Tags, Options: menu.FromStoreOptions(it.Options), UpdatedAt: it.UpdatedAt}
	if v.Tags == nil {
		v.Tags = []string{}
	}
	if it.ImageID != "" {
		v.ImagePath = "/v1/instances/" + instanceID + "/menu/items/" + it.ID + "/image?v=" + it.ImageID[len("mimg_"):]
	}
	return v
}

// Get returns the whole menu, in display order.
func (m *Menu) Get(ctx context.Context, instanceID string) (MenuView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuView{}, err
	}
	cats, err := m.Store.ListMenuCategories(ctx, instanceID)
	if err != nil {
		return MenuView{}, err
	}
	items, err := m.Store.ListMenuItems(ctx, instanceID)
	if err != nil {
		return MenuView{}, err
	}
	v := MenuView{Categories: []MenuCategoryView{}, Uncategorized: []MenuItemView{}, ItemCount: len(items),
		Limits: MenuLimits{MaxItems: store.MaxMenuItems, MaxCategories: store.MaxMenuCategories, MaxOptions: store.MaxMenuOptions,
			MaxChoices: store.MaxMenuChoices}}
	idx := map[string]int{}
	for _, c := range cats {
		idx[c.ID] = len(v.Categories)
		v.Categories = append(v.Categories, MenuCategoryView{ID: c.ID, Name: c.Name, Position: c.Position, Items: []MenuItemView{}})
	}
	for _, it := range items {
		if i, ok := idx[it.CategoryID]; ok {
			v.Categories[i].Items = append(v.Categories[i].Items, itemView(instanceID, it))
		} else {
			v.Uncategorized = append(v.Uncategorized, itemView(instanceID, it))
		}
	}
	return v, nil
}

// Item returns one item.
func (m *Menu) Item(ctx context.Context, instanceID, itemID string) (MenuItemView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuItemView{}, err
	}
	it, err := m.Store.GetMenuItem(ctx, instanceID, itemID)
	if err != nil {
		return MenuItemView{}, menuErr(err, "menu item")
	}
	return itemView(instanceID, it), nil
}

// ---- categories ----

// MenuCategoryInput creates or renames a category.
type MenuCategoryInput struct {
	Name string `json:"name"`
}

func categoryName(s string) (string, error) {
	n := menu.CleanLine(s)
	if n == "" || utf8.RuneCountInString(n) > menu.MaxCategoryLen {
		return "", apierr.Validation(fmt.Sprintf("name is required (at most %d characters)", menu.MaxCategoryLen))
	}
	return n, nil
}

func (m *Menu) CreateCategory(ctx context.Context, instanceID string, in MenuCategoryInput) (MenuCategoryView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuCategoryView{}, err
	}
	name, err := categoryName(in.Name)
	if err != nil {
		return MenuCategoryView{}, err
	}
	c, err := m.Store.CreateMenuCategory(ctx, instanceID, name)
	if err != nil {
		return MenuCategoryView{}, menuErr(err, "menu category")
	}
	return MenuCategoryView{ID: c.ID, Name: c.Name, Position: c.Position, Items: []MenuItemView{}}, nil
}

func (m *Menu) RenameCategory(ctx context.Context, instanceID, catID string, in MenuCategoryInput) (MenuCategoryView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuCategoryView{}, err
	}
	name, err := categoryName(in.Name)
	if err != nil {
		return MenuCategoryView{}, err
	}
	if err := m.Store.RenameMenuCategory(ctx, instanceID, catID, name); err != nil {
		return MenuCategoryView{}, menuErr(err, "menu category")
	}
	c, err := m.Store.GetMenuCategory(ctx, instanceID, catID)
	if err != nil {
		return MenuCategoryView{}, menuErr(err, "menu category")
	}
	return MenuCategoryView{ID: c.ID, Name: c.Name, Position: c.Position, Items: []MenuItemView{}}, nil
}

// DeleteCategory removes a category; its items stay, without category.
func (m *Menu) DeleteCategory(ctx context.Context, instanceID, catID string) error {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return err
	}
	return menuErr(m.Store.DeleteMenuCategory(ctx, instanceID, catID), "menu category")
}

// ReorderInput lists ids in their new order (items: optionally into category_id).
type ReorderInput struct {
	IDs        []string `json:"ids"`
	CategoryID *string  `json:"category_id,omitempty"`
}

func checkIDs(ids []string, limit int) error {
	if len(ids) == 0 || len(ids) > limit {
		return apierr.Validation(fmt.Sprintf("ids lists 1 to %d ids in their new order", limit))
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return apierr.Validation("ids has a repeated id")
		}
		seen[id] = true
	}
	return nil
}

func (m *Menu) ReorderCategories(ctx context.Context, instanceID string, in ReorderInput) error {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return err
	}
	if err := checkIDs(in.IDs, store.MaxMenuCategories); err != nil {
		return err
	}
	return menuErr(m.Store.ReorderMenuCategories(ctx, instanceID, in.IDs), "menu category")
}

// ReorderItems puts the listed items, in order, in category_id (required: "" for no
// category).
func (m *Menu) ReorderItems(ctx context.Context, instanceID string, in ReorderInput) error {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return err
	}
	if in.CategoryID == nil {
		return apierr.Validation(`category_id is required ("" for items without category)`)
	}
	if err := checkIDs(in.IDs, store.MaxMenuItems); err != nil {
		return err
	}
	return menuErr(m.Store.ReorderMenuItems(ctx, instanceID, *in.CategoryID, in.IDs), "menu item or category")
}

// ---- items ----

// MenuItemInput creates an item, or patches one (absent fields stay as they are).
// The price is price_cents, or price as text ("45,90", "R$ 1.234,50") or a number of
// reais (45.9).
type MenuItemInput struct {
	CategoryID  *string         `json:"category_id,omitempty"`
	Name        *string         `json:"name,omitempty"`
	Description *string         `json:"description,omitempty"`
	PriceCents  *int64          `json:"price_cents,omitempty"`
	Price       json.RawMessage `json:"price,omitempty"`
	Currency    *string         `json:"currency,omitempty"`
	Available   *bool           `json:"available,omitempty"`
	SKU         *string         `json:"sku,omitempty"`
	Tags        *[]string       `json:"tags,omitempty"`
	Options     *[]menu.Option  `json:"options,omitempty"`
	// ImageURL downloads the item's photo (http/https; resized server side).
	ImageURL string `json:"image_url,omitempty"`
}

// apply merges the input into a row and validates it.
func (in MenuItemInput) apply(r *menu.Row, currency *string) error {
	if in.Name != nil {
		r.Name = *in.Name
	}
	if in.Description != nil {
		r.Description = *in.Description
	}
	if in.SKU != nil {
		r.SKU = *in.SKU
	}
	if in.Tags != nil {
		r.Tags = *in.Tags
	}
	if in.Available != nil {
		r.Available = *in.Available
	}
	if in.Options != nil {
		r.Options = *in.Options
	}
	if in.PriceCents != nil && len(in.Price) > 0 {
		return apierr.Validation("send price_cents or price, not both")
	}
	if in.PriceCents != nil {
		r.PriceCents = *in.PriceCents
	}
	if len(in.Price) > 0 && string(in.Price) != "null" {
		var f float64
		var s string
		var err error
		switch {
		case json.Unmarshal(in.Price, &f) == nil:
			r.PriceCents, err = menu.PriceFromFloat(f)
		case json.Unmarshal(in.Price, &s) == nil:
			r.PriceCents, err = menu.ParsePrice(s)
		default:
			err = menu.ErrPrice
		}
		if err != nil {
			return apierr.Validation(`price must be like "45,90" or "R$ 1.234,50" (or price_cents)`)
		}
	}
	if in.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*in.Currency))
		if len(c) != 3 || strings.Trim(c, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
			return apierr.Validation("currency is a 3-letter code (BRL)")
		}
		*currency = c
	}
	if in.ImageURL != "" && len(in.ImageURL) > menu.MaxImageURLLen {
		return apierr.Validation("image_url is too long")
	}
	menu.NormalizeRow(r)
	if errs := menu.ValidateRow(*r); len(errs) > 0 {
		return apierr.Validation(strings.Join(errs, "; "))
	}
	return nil
}

func rowOf(it store.MenuItem) menu.Row {
	return menu.Row{Name: it.Name, Description: it.Description, PriceCents: it.PriceCents, Available: it.Available, SKU: it.SKU,
		Tags: it.Tags, Options: menu.FromStoreOptions(it.Options)}
}

func (m *Menu) CreateItem(ctx context.Context, instanceID string, in MenuItemInput) (MenuItemView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuItemView{}, err
	}
	if in.Name == nil {
		return MenuItemView{}, apierr.Validation("name is required")
	}
	r := menu.Row{Available: true}
	currency := "BRL"
	if err := in.apply(&r, &currency); err != nil {
		return MenuItemView{}, err
	}
	it := menu.RowToItem(r)
	it.Currency = currency
	if in.CategoryID != nil {
		it.CategoryID = *in.CategoryID
	}
	created, err := m.Store.CreateMenuItem(ctx, instanceID, it)
	if err != nil {
		return MenuItemView{}, menuErr(err, "menu category")
	}
	if in.ImageURL != "" {
		if _, err := m.SetImage(ctx, instanceID, created.ID, MenuImageInput{URL: in.ImageURL}); err != nil {
			// The item is saved; say why its photo is missing.
			return itemView(instanceID, created), apierr.New(http.StatusUnprocessableEntity, "menu_image_failed",
				"item created ("+created.ID+") but its image could not be saved: "+errMessage(err), false)
		}
		return m.Item(ctx, instanceID, created.ID)
	}
	return itemView(instanceID, created), nil
}

func errMessage(err error) string {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae.Message
	}
	return "internal error"
}

func (m *Menu) UpdateItem(ctx context.Context, instanceID, itemID string, in MenuItemInput) (MenuItemView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuItemView{}, err
	}
	cur, err := m.Store.GetMenuItem(ctx, instanceID, itemID)
	if err != nil {
		return MenuItemView{}, menuErr(err, "menu item")
	}
	r := rowOf(cur)
	currency := cur.Currency
	if err := in.apply(&r, &currency); err != nil {
		return MenuItemView{}, err
	}
	it := menu.RowToItem(r)
	it.ID, it.Currency, it.CategoryID = cur.ID, currency, cur.CategoryID
	if in.CategoryID != nil {
		it.CategoryID = *in.CategoryID
	}
	if in.Options == nil {
		it.Options = nil // keep the stored ones (their ids)
	}
	if _, err := m.Store.UpdateMenuItem(ctx, instanceID, it); err != nil {
		return MenuItemView{}, menuErr(err, "menu item or category")
	}
	if in.ImageURL != "" {
		if _, err := m.SetImage(ctx, instanceID, itemID, MenuImageInput{URL: in.ImageURL}); err != nil {
			return MenuItemView{}, err
		}
	}
	return m.Item(ctx, instanceID, itemID)
}

// SetAvailable toggles "esgotado".
func (m *Menu) SetAvailable(ctx context.Context, instanceID, itemID string, available bool) (MenuItemView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuItemView{}, err
	}
	if err := m.Store.SetMenuItemAvailable(ctx, instanceID, itemID, available); err != nil {
		return MenuItemView{}, menuErr(err, "menu item")
	}
	return m.Item(ctx, instanceID, itemID)
}

func (m *Menu) DeleteItem(ctx context.Context, instanceID, itemID string) error {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return err
	}
	img, err := m.Store.DeleteMenuItem(ctx, instanceID, itemID)
	if err != nil {
		return menuErr(err, "menu item")
	}
	m.removeImage(instanceID, img)
	return nil
}

// ---- options ----

func checkOption(o menu.Option) (menu.Option, error) {
	opts := menu.NormalizeOptions([]menu.Option{o})
	if err := menu.ValidateOptions(opts); err != nil {
		return menu.Option{}, apierr.Validation(err.Error())
	}
	return opts[0], nil
}

func (m *Menu) AddOption(ctx context.Context, instanceID, itemID string, in menu.Option) (menu.Option, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return menu.Option{}, err
	}
	o, err := checkOption(in)
	if err != nil {
		return menu.Option{}, err
	}
	saved, err := m.Store.AddMenuOption(ctx, instanceID, itemID, menu.ToStoreOptions([]menu.Option{o})[0])
	if err != nil {
		return menu.Option{}, menuErr(err, "menu item")
	}
	return menu.FromStoreOptions([]store.MenuOption{saved})[0], nil
}

func (m *Menu) UpdateOption(ctx context.Context, instanceID, itemID, optID string, in menu.Option) (menu.Option, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return menu.Option{}, err
	}
	o, err := checkOption(in)
	if err != nil {
		return menu.Option{}, err
	}
	so := menu.ToStoreOptions([]menu.Option{o})[0]
	so.ID = optID
	if err := m.Store.UpdateMenuOption(ctx, instanceID, itemID, so); err != nil {
		return menu.Option{}, menuErr(err, "menu option")
	}
	o.ID = optID
	return o, nil
}

func (m *Menu) DeleteOption(ctx context.Context, instanceID, itemID, optID string) error {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return err
	}
	return menuErr(m.Store.DeleteMenuOption(ctx, instanceID, itemID, optID), "menu option")
}

// ---- images ----

// MenuImageInput is a photo to set: url (downloaded with the media rules) or base64.
type MenuImageInput struct {
	URL    string `json:"url,omitempty"`
	Base64 string `json:"base64,omitempty"`
}

func (m *Menu) imageDir(instanceID string) string {
	return filepath.Join(m.DataDir, "menu", instanceID)
}

func (m *Menu) imageFile(instanceID, imageID string) (string, bool) {
	if !validMenuID("mimg", imageID) {
		return "", false
	}
	return filepath.Join(m.imageDir(instanceID), imageID+".jpg"), true
}

func (m *Menu) removeImage(instanceID, imageID string) {
	if f, ok := m.imageFile(instanceID, imageID); ok {
		_ = os.Remove(f)
	}
}

// fetchImage downloads or decodes a photo and normalizes it (JPEG, at most 1080 px).
func (m *Menu) fetchImage(ctx context.Context, in MenuImageInput) ([]byte, error) {
	if (in.URL == "") == (in.Base64 == "") {
		return nil, apierr.Validation("send exactly one of url or base64")
	}
	var data []byte
	var err error
	if in.URL != "" {
		if m.Fetcher == nil {
			return nil, apierr.New(http.StatusNotImplemented, "not_implemented", "image downloads are not available on this server", false)
		}
		data, _, err = m.Fetcher.Fetch(ctx, in.URL, menu.MaxImageBytes)
	} else {
		data, _, err = media.DecodeBase64(in.Base64, menu.MaxImageBytes)
	}
	switch {
	case errors.Is(err, media.ErrTooLarge):
		return nil, apierr.New(http.StatusRequestEntityTooLarge, "media_too_large", "the image exceeds 8 MB", false)
	case errors.Is(err, media.ErrBlocked):
		return nil, apierr.Validation("url points to a private or local address")
	case errors.Is(err, media.ErrInvalidURL):
		return nil, apierr.Validation(err.Error())
	case errors.Is(err, media.ErrFetch):
		return nil, apierr.New(http.StatusUnprocessableEntity, "media_fetch_failed", "the image url could not be downloaded", true)
	case err != nil:
		return nil, apierr.Validation(err.Error())
	}
	return m.normalize(ctx, data)
}

func (m *Menu) normalize(ctx context.Context, data []byte) ([]byte, error) {
	release, err := m.acquireImage(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	out, err := menu.NormalizeImage(data)
	if err != nil {
		return nil, apierr.Validation(err.Error())
	}
	return out, nil
}

// storeImage writes a normalized photo and points the item at it.
func (m *Menu) storeImage(ctx context.Context, instanceID, itemID string, jpg []byte) error {
	if err := os.MkdirAll(m.imageDir(instanceID), 0o700); err != nil {
		return err
	}
	imageID := secure.NewID("mimg_")
	file, _ := m.imageFile(instanceID, imageID)
	if err := os.WriteFile(file, jpg, 0o600); err != nil {
		return err
	}
	prev, err := m.Store.SetMenuItemImage(ctx, instanceID, itemID, imageID)
	if err != nil {
		_ = os.Remove(file)
		return menuErr(err, "menu item")
	}
	m.removeImage(instanceID, prev)
	return nil
}

// SetImage sets an item's photo.
func (m *Menu) SetImage(ctx context.Context, instanceID, itemID string, in MenuImageInput) (MenuItemView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuItemView{}, err
	}
	if _, err := m.Store.GetMenuItem(ctx, instanceID, itemID); err != nil {
		return MenuItemView{}, menuErr(err, "menu item")
	}
	jpg, err := m.fetchImage(ctx, in)
	if err != nil {
		return MenuItemView{}, err
	}
	if err := m.storeImage(ctx, instanceID, itemID, jpg); err != nil {
		return MenuItemView{}, err
	}
	return m.Item(ctx, instanceID, itemID)
}

// DeleteImage removes an item's photo.
func (m *Menu) DeleteImage(ctx context.Context, instanceID, itemID string) (MenuItemView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuItemView{}, err
	}
	prev, err := m.Store.SetMenuItemImage(ctx, instanceID, itemID, "")
	if err != nil {
		return MenuItemView{}, menuErr(err, "menu item")
	}
	m.removeImage(instanceID, prev)
	return m.Item(ctx, instanceID, itemID)
}

// Image returns an item's photo (JPEG).
func (m *Menu) Image(ctx context.Context, instanceID, itemID string) ([]byte, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return nil, err
	}
	it, err := m.Store.GetMenuItem(ctx, instanceID, itemID)
	if err != nil {
		return nil, menuErr(err, "menu item")
	}
	return readMenuImage(m.DataDir, instanceID, it.ImageID)
}

func readMenuImage(dataDir, instanceID, imageID string) ([]byte, error) {
	if !validMenuID("mimg", imageID) {
		return nil, notFound("image")
	}
	b, err := os.ReadFile(filepath.Join(dataDir, "menu", instanceID, imageID+".jpg"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, notFound("image")
	}
	return b, err
}

// ---- text ----

// MenuTextInput chooses what the menu text shows.
type MenuTextInput struct {
	CategoryID         string `json:"category_id,omitempty"`
	IncludeUnavailable bool   `json:"include_unavailable,omitempty"`
	Title              string `json:"title,omitempty"`
}

// MenuTextView is the menu as the WhatsApp messages it would be sent as.
type MenuTextView struct {
	Chunks []string `json:"chunks"`
}

func (m *Menu) chunks(ctx context.Context, instanceID string, in MenuTextInput) ([]string, error) {
	cats, err := m.Store.ListMenuCategories(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if in.CategoryID != "" {
		found := false
		for _, c := range cats {
			found = found || c.ID == in.CategoryID
		}
		if !found {
			return nil, notFound("menu category")
		}
	}
	items, err := m.Store.ListMenuItems(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	title := menu.CleanLine(in.Title)
	if utf8.RuneCountInString(title) > 60 {
		return nil, apierr.Validation("title is limited to 60 characters")
	}
	return menu.Format(cats, items, menu.FormatOptions{Title: title, CategoryID: in.CategoryID, IncludeUnavailable: in.IncludeUnavailable}), nil
}

// Text renders the menu as WhatsApp text (one entry per message).
func (m *Menu) Text(ctx context.Context, instanceID string, in MenuTextInput) (MenuTextView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuTextView{}, err
	}
	c, err := m.chunks(ctx, instanceID, in)
	if err != nil {
		return MenuTextView{}, err
	}
	if c == nil {
		c = []string{}
	}
	return MenuTextView{Chunks: c}, nil
}

// ---- sends ----

// SendMenuInput sends the menu as text messages, split when long.
type SendMenuInput struct {
	To                 string `json:"to"`
	CategoryID         string `json:"category_id,omitempty"`
	IncludeUnavailable bool   `json:"include_unavailable,omitempty"`
	Title              string `json:"title,omitempty"`
	// MaxMessages refuses a menu that needs more messages (default and max 10).
	MaxMessages     int    `json:"max_messages,omitempty"`
	ReplyTo         string `json:"reply_to,omitempty"`
	ClientMessageID string `json:"client_message_id,omitempty"`
	ExpiresAfter    string `json:"expires_after,omitempty"`
}

// SendMenuResult lists the queued messages, in order.
type SendMenuResult struct {
	Messages []MessageView `json:"messages"`
}

// SendMenu queues the menu text (one message per chunk, through the send queue). A
// client_message_id makes it idempotent: chunk n uses "<id>:n".
func (m *Menu) SendMenu(ctx context.Context, instanceID string, in SendMenuInput, wait time.Duration) (SendMenuResult, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return SendMenuResult{}, err
	}
	if m.Sender == nil {
		return SendMenuResult{}, apierr.New(http.StatusNotImplemented, "not_implemented", "sending is not available", false)
	}
	if in.MaxMessages == 0 {
		in.MaxMessages = defaultMenuMsgs
	}
	if in.MaxMessages < 1 || in.MaxMessages > menu.MaxChunks {
		return SendMenuResult{}, apierr.Validation(fmt.Sprintf("max_messages must be between 1 and %d", menu.MaxChunks))
	}
	if len(in.ClientMessageID) > 120 {
		return SendMenuResult{}, apierr.Validation("client_message_id is limited to 120 characters here")
	}
	chunks, err := m.chunks(ctx, instanceID, MenuTextInput{CategoryID: in.CategoryID, IncludeUnavailable: in.IncludeUnavailable, Title: in.Title})
	if err != nil {
		return SendMenuResult{}, err
	}
	if len(chunks) == 0 {
		return SendMenuResult{}, apierr.Validation("the menu has no available items to send")
	}
	if len(chunks) > in.MaxMessages {
		return SendMenuResult{}, apierr.Validation(fmt.Sprintf("the menu needs %d messages and max_messages is %d: send one category (category_id) instead",
			len(chunks), in.MaxMessages))
	}
	res := SendMenuResult{Messages: []MessageView{}}
	for i, text := range chunks {
		si := SendInput{To: in.To, Type: "text", Text: text, ExpiresAfter: in.ExpiresAfter}
		if i == 0 {
			si.ReplyTo = in.ReplyTo
		}
		if in.ClientMessageID != "" {
			si.ClientMessageID = fmt.Sprintf("%s:%d", in.ClientMessageID, i+1)
		}
		v, err := m.Sender.Send(ctx, instanceID, si, 0)
		if err != nil {
			if i == 0 {
				return SendMenuResult{}, err
			}
			return res, apierr.New(http.StatusConflict, "partially_queued",
				fmt.Sprintf("%d of %d menu messages were queued; the rest failed: %s", i, len(chunks), errMessage(err)), true)
		}
		res.Messages = append(res.Messages, v)
	}
	if wait > 0 && len(res.Messages) > 0 {
		last := res.Messages[len(res.Messages)-1]
		if w, ok := m.Sender.(interface {
			Wait(ctx context.Context, instanceID, id string, wait time.Duration) (MessageView, error)
		}); ok && (last.Status == "queued" || last.Status == "in_flight") {
			if v, err := w.Wait(ctx, instanceID, last.ID, wait); err == nil {
				res.Messages[len(res.Messages)-1] = v
			}
		}
	}
	return res, nil
}
