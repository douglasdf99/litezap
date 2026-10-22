package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/menu"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- building the Cardápio with the user's own assistant ----
//
// These tools let the assistant the user already pays for (Claude Desktop/Code, ChatGPT)
// build and edit the menu: it reads the user's text, photo or spreadsheet itself and
// sends rows here. They are for the user's assistant only: the LiteZap agents never get
// them (ToolDef.AssistantOnly, left out of AgentCatalog). Every write goes through the
// same service validations and limits as the dashboard and the REST API.

// MenuWriter is the editing part of the Cardápio (optional in a Backend).
type MenuWriter interface {
	CreateMenuCategory(ctx context.Context, id, name string) (service.MenuCategoryView, error)
	RenameMenuCategory(ctx context.Context, id, catID, name string) (service.MenuCategoryView, error)
	DeleteMenuCategory(ctx context.Context, id, catID string) error
	ReorderMenuCategories(ctx context.Context, id string, ids []string) error
	ReorderMenuItems(ctx context.Context, id string, in service.ReorderInput) error
	CreateMenuItem(ctx context.Context, id string, in service.MenuItemInput) (service.MenuItemView, error)
	UpdateMenuItem(ctx context.Context, id, itemID string, in service.MenuItemInput) (service.MenuItemView, error)
	DeleteMenuItem(ctx context.Context, id, itemID string) error
	SetMenuItemAvailable(ctx context.Context, id, itemID string, available bool) (service.MenuItemView, error)
	AddMenuOption(ctx context.Context, id, itemID string, o menu.Option) (menu.Option, error)
	UpdateMenuOption(ctx context.Context, id, itemID, optID string, o menu.Option) (menu.Option, error)
	DeleteMenuOption(ctx context.Context, id, itemID, optID string) error
	SetMenuItemImageURL(ctx context.Context, id, itemID, imageURL string) (service.MenuItemView, error)
	ImportMenuRows(ctx context.Context, id string, in service.MenuRowsInput) (service.MenuImportView, error)
}

var (
	_ MenuWriter = (*Local)(nil)
	_ MenuWriter = (*REST)(nil)
)

// ---- Local ----

func (l *Local) CreateMenuCategory(ctx context.Context, id, name string) (service.MenuCategoryView, error) {
	m, err := l.menuSvc(id)
	if err != nil {
		return service.MenuCategoryView{}, err
	}
	return m.CreateCategory(ctx, id, service.MenuCategoryInput{Name: name})
}

func (l *Local) RenameMenuCategory(ctx context.Context, id, catID, name string) (service.MenuCategoryView, error) {
	m, err := l.menuSvc(id)
	if err != nil {
		return service.MenuCategoryView{}, err
	}
	return m.RenameCategory(ctx, id, catID, service.MenuCategoryInput{Name: name})
}

func (l *Local) DeleteMenuCategory(ctx context.Context, id, catID string) error {
	m, err := l.menuSvc(id)
	if err != nil {
		return err
	}
	return m.DeleteCategory(ctx, id, catID)
}

func (l *Local) ReorderMenuCategories(ctx context.Context, id string, ids []string) error {
	m, err := l.menuSvc(id)
	if err != nil {
		return err
	}
	return m.ReorderCategories(ctx, id, service.ReorderInput{IDs: ids})
}

func (l *Local) ReorderMenuItems(ctx context.Context, id string, in service.ReorderInput) error {
	m, err := l.menuSvc(id)
	if err != nil {
		return err
	}
	return m.ReorderItems(ctx, id, in)
}

func (l *Local) CreateMenuItem(ctx context.Context, id string, in service.MenuItemInput) (service.MenuItemView, error) {
	m, err := l.menuSvc(id)
	if err != nil {
		return service.MenuItemView{}, err
	}
	return m.CreateItem(ctx, id, in)
}

func (l *Local) UpdateMenuItem(ctx context.Context, id, itemID string, in service.MenuItemInput) (service.MenuItemView, error) {
	m, err := l.menuSvc(id)
	if err != nil {
		return service.MenuItemView{}, err
	}
	return m.UpdateItem(ctx, id, itemID, in)
}

func (l *Local) DeleteMenuItem(ctx context.Context, id, itemID string) error {
	m, err := l.menuSvc(id)
	if err != nil {
		return err
	}
	return m.DeleteItem(ctx, id, itemID)
}

func (l *Local) SetMenuItemAvailable(ctx context.Context, id, itemID string, available bool) (service.MenuItemView, error) {
	m, err := l.menuSvc(id)
	if err != nil {
		return service.MenuItemView{}, err
	}
	return m.SetAvailable(ctx, id, itemID, available)
}

func (l *Local) AddMenuOption(ctx context.Context, id, itemID string, o menu.Option) (menu.Option, error) {
	m, err := l.menuSvc(id)
	if err != nil {
		return menu.Option{}, err
	}
	return m.AddOption(ctx, id, itemID, o)
}

func (l *Local) UpdateMenuOption(ctx context.Context, id, itemID, optID string, o menu.Option) (menu.Option, error) {
	m, err := l.menuSvc(id)
	if err != nil {
		return menu.Option{}, err
	}
	return m.UpdateOption(ctx, id, itemID, optID, o)
}

func (l *Local) DeleteMenuOption(ctx context.Context, id, itemID, optID string) error {
	m, err := l.menuSvc(id)
	if err != nil {
		return err
	}
	return m.DeleteOption(ctx, id, itemID, optID)
}

func (l *Local) SetMenuItemImageURL(ctx context.Context, id, itemID, imageURL string) (service.MenuItemView, error) {
	m, err := l.menuSvc(id)
	if err != nil {
		return service.MenuItemView{}, err
	}
	return m.SetImage(ctx, id, itemID, service.MenuImageInput{URL: imageURL})
}

func (l *Local) ImportMenuRows(ctx context.Context, id string, in service.MenuRowsInput) (service.MenuImportView, error) {
	m, err := l.menuSvc(id)
	if err != nil {
		return service.MenuImportView{}, err
	}
	return m.ImportRows(ctx, id, in)
}

// ---- REST (the routes the dashboard uses) ----

var menuRefRe = regexp.MustCompile(`^(mcat|mitm|mopt)_[0-9a-f]{16}$`)

// menuRef checks an id before it goes into a path.
func menuRef(prefix, id, what string) (string, error) {
	if !strings.HasPrefix(id, prefix+"_") || !menuRefRe.MatchString(id) {
		return "", apierr.Validation(fmt.Sprintf("%s is the id of a %s (%s_…, from get_menu or get_menu_item)", what, strings.ReplaceAll(what, "_id", ""), prefix))
	}
	return url.PathEscape(id), nil
}

// importTimeout covers the photos an import may download (up to 90s server side).
const importTimeout = 150 * time.Second

func (r *REST) CreateMenuCategory(ctx context.Context, id, name string) (service.MenuCategoryView, error) {
	var v service.MenuCategoryView
	err := r.call(ctx, restTimeout, "POST", instPath(id, "menu", "categories"), nil, service.MenuCategoryInput{Name: name}, &v)
	return v, err
}

func (r *REST) RenameMenuCategory(ctx context.Context, id, catID, name string) (service.MenuCategoryView, error) {
	seg, err := menuRef("mcat", catID, "category_id")
	if err != nil {
		return service.MenuCategoryView{}, err
	}
	var v service.MenuCategoryView
	err = r.call(ctx, restTimeout, "PATCH", instPath(id, "menu", "categories", seg), nil, service.MenuCategoryInput{Name: name}, &v)
	return v, err
}

func (r *REST) DeleteMenuCategory(ctx context.Context, id, catID string) error {
	seg, err := menuRef("mcat", catID, "category_id")
	if err != nil {
		return err
	}
	return r.call(ctx, restTimeout, "DELETE", instPath(id, "menu", "categories", seg), nil, nil, nil)
}

func (r *REST) ReorderMenuCategories(ctx context.Context, id string, ids []string) error {
	return r.call(ctx, restTimeout, "PUT", instPath(id, "menu", "categories", "order"), nil, service.ReorderInput{IDs: ids}, nil)
}

func (r *REST) ReorderMenuItems(ctx context.Context, id string, in service.ReorderInput) error {
	return r.call(ctx, restTimeout, "PUT", instPath(id, "menu", "items", "order"), nil, in, nil)
}

func (r *REST) CreateMenuItem(ctx context.Context, id string, in service.MenuItemInput) (service.MenuItemView, error) {
	var v service.MenuItemView
	err := r.call(ctx, restTimeout, "POST", instPath(id, "menu", "items"), nil, in, &v)
	return v, err
}

func (r *REST) UpdateMenuItem(ctx context.Context, id, itemID string, in service.MenuItemInput) (service.MenuItemView, error) {
	seg, err := menuRef("mitm", itemID, "item_id")
	if err != nil {
		return service.MenuItemView{}, err
	}
	var v service.MenuItemView
	err = r.call(ctx, restTimeout, "PATCH", instPath(id, "menu", "items", seg), nil, in, &v)
	return v, err
}

func (r *REST) DeleteMenuItem(ctx context.Context, id, itemID string) error {
	seg, err := menuRef("mitm", itemID, "item_id")
	if err != nil {
		return err
	}
	return r.call(ctx, restTimeout, "DELETE", instPath(id, "menu", "items", seg), nil, nil, nil)
}

func (r *REST) SetMenuItemAvailable(ctx context.Context, id, itemID string, available bool) (service.MenuItemView, error) {
	seg, err := menuRef("mitm", itemID, "item_id")
	if err != nil {
		return service.MenuItemView{}, err
	}
	var v service.MenuItemView
	err = r.call(ctx, restTimeout, "PUT", instPath(id, "menu", "items", seg, "available"), nil, map[string]bool{"available": available}, &v)
	return v, err
}

func (r *REST) AddMenuOption(ctx context.Context, id, itemID string, o menu.Option) (menu.Option, error) {
	seg, err := menuRef("mitm", itemID, "item_id")
	if err != nil {
		return menu.Option{}, err
	}
	var v menu.Option
	err = r.call(ctx, restTimeout, "POST", instPath(id, "menu", "items", seg, "options"), nil, o, &v)
	return v, err
}

func (r *REST) UpdateMenuOption(ctx context.Context, id, itemID, optID string, o menu.Option) (menu.Option, error) {
	seg, err := menuRef("mitm", itemID, "item_id")
	if err != nil {
		return menu.Option{}, err
	}
	oseg, err := menuRef("mopt", optID, "option_id")
	if err != nil {
		return menu.Option{}, err
	}
	var v menu.Option
	err = r.call(ctx, restTimeout, "PATCH", instPath(id, "menu", "items", seg, "options", oseg), nil, o, &v)
	return v, err
}

func (r *REST) DeleteMenuOption(ctx context.Context, id, itemID, optID string) error {
	seg, err := menuRef("mitm", itemID, "item_id")
	if err != nil {
		return err
	}
	oseg, err := menuRef("mopt", optID, "option_id")
	if err != nil {
		return err
	}
	return r.call(ctx, restTimeout, "DELETE", instPath(id, "menu", "items", seg, "options", oseg), nil, nil, nil)
}

func (r *REST) SetMenuItemImageURL(ctx context.Context, id, itemID, imageURL string) (service.MenuItemView, error) {
	seg, err := menuRef("mitm", itemID, "item_id")
	if err != nil {
		return service.MenuItemView{}, err
	}
	var v service.MenuItemView
	err = r.call(ctx, restTimeout, "PUT", instPath(id, "menu", "items", seg, "image"), nil, service.MenuImageInput{URL: imageURL}, &v)
	return v, err
}

func (r *REST) ImportMenuRows(ctx context.Context, id string, in service.MenuRowsInput) (service.MenuImportView, error) {
	var v service.MenuImportView
	err := r.call(ctx, importTimeout, "POST", instPath(id, "menu", "import", "rows"), nil, in, &v)
	return v, err
}

// ---- tool inputs and outputs ----

type menuChoiceIn struct {
	Name            string `json:"name" jsonschema:"Choice name, e.g. \"G\" or \"Catupiry\"."`
	PriceDelta      string `json:"price_delta,omitempty" jsonschema:"Price difference as written: \"+10,00\", \"-2,00\", \"5\" (reais). Omit when the choice does not change the price."`
	PriceDeltaCents int64  `json:"price_delta_cents,omitempty" jsonschema:"Price difference in cents (alternative to price_delta)."`
}

type menuOptionIn struct {
	Name     string         `json:"name" jsonschema:"Option group name, e.g. \"Tamanho\", \"Borda\", \"Adicionais\"."`
	Required bool           `json:"required,omitempty" jsonschema:"The customer must pick at least one choice."`
	Min      int            `json:"min,omitempty" jsonschema:"Minimum choices (default 0, or 1 when required)."`
	Max      int            `json:"max,omitempty" jsonschema:"Maximum choices (default 1)."`
	Choices  []menuChoiceIn `json:"choices" jsonschema:"The choices (1 to 20)."`
}

// option converts an input group, reading its price differences.
func (o menuOptionIn) option() (menu.Option, error) {
	out := menu.Option{Name: o.Name, Required: o.Required, Min: o.Min, Max: o.Max, Choices: []menu.Choice{}}
	for _, c := range o.Choices {
		cents := c.PriceDeltaCents
		if d := strings.TrimSpace(c.PriceDelta); d != "" {
			neg := strings.HasPrefix(d, "-")
			v, err := menu.ParsePrice(strings.TrimPrefix(strings.TrimPrefix(d, "-"), "+"))
			if err != nil {
				return menu.Option{}, apierr.Validation(fmt.Sprintf("option %q, choice %q: price_delta %q is not a price (use \"+10,00\")", o.Name, c.Name, d))
			}
			if c.PriceDeltaCents != 0 && c.PriceDeltaCents != v && c.PriceDeltaCents != -v {
				return menu.Option{}, apierr.Validation(fmt.Sprintf("option %q, choice %q: send price_delta or price_delta_cents, not both", o.Name, c.Name))
			}
			if cents = v; neg {
				cents = -v
			}
		}
		out.Choices = append(out.Choices, menu.Choice{Name: c.Name, PriceDeltaCents: cents})
	}
	return out, nil
}

func optionsOf(in []menuOptionIn) ([]menu.Option, error) {
	out := make([]menu.Option, 0, len(in))
	for _, o := range in {
		v, err := o.option()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

type menuCategoryNameInput struct {
	InstanceID string `json:"instance_id,omitempty" jsonschema:"Instance whose menu to edit"`
	Name       string `json:"name" jsonschema:"Category name, e.g. \"Pizzas\" (up to 60 characters)."`
}

type menuCategoryRenameInput struct {
	InstanceID string `json:"instance_id,omitempty" jsonschema:"Instance whose menu to edit"`
	CategoryID string `json:"category_id" jsonschema:"Category id (mcat_…) from get_menu."`
	Name       string `json:"name" jsonschema:"New name (up to 60 characters)."`
}

type menuCategoryRefInput struct {
	InstanceID string `json:"instance_id,omitempty" jsonschema:"Instance whose menu to edit"`
	CategoryID string `json:"category_id" jsonschema:"Category id (mcat_…) from get_menu."`
}

type menuCategoryResult struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Note string `json:"note,omitempty"`
}

type createMenuItemInput struct {
	InstanceID     string         `json:"instance_id,omitempty" jsonschema:"Instance whose menu to edit"`
	Name           string         `json:"name" jsonschema:"Item name, e.g. \"Pizza Calabresa\" (up to 120 characters)."`
	Description    string         `json:"description,omitempty" jsonschema:"Description (up to 1000 characters)."`
	Price          string         `json:"price,omitempty" jsonschema:"Price as the user wrote it, in reais: \"45,90\", \"R$ 1.234,50\", \"45\". Never invent one: omit it only when the user gave no price (the item shows no price)."`
	PriceCents     int64          `json:"price_cents,omitempty" jsonschema:"Price in cents (alternative to price): 4590 = R$ 45,90."`
	Category       string         `json:"category,omitempty" jsonschema:"Category by name (matched ignoring case). Alternative to category_id."`
	CategoryID     string         `json:"category_id,omitempty" jsonschema:"Category id (mcat_…) from get_menu."`
	CreateCategory bool           `json:"create_category,omitempty" jsonschema:"Create the category named in category when it does not exist yet (default false: an unknown name is an error)."`
	Available      *bool          `json:"available,omitempty" jsonschema:"false marks it sold out (esgotado). Default true."`
	Tags           []string       `json:"tags,omitempty" jsonschema:"Short tags, e.g. vegano, sem gluten (up to 10)."`
	ImageURL       string         `json:"image_url,omitempty" jsonschema:"Optional public http(s) URL of a photo, downloaded and resized by LiteZap."`
	Options        []menuOptionIn `json:"options,omitempty" jsonschema:"Option groups (up to 10), e.g. Tamanho: P, M, G +10,00."`
}

type updateMenuItemInput struct {
	InstanceID     string          `json:"instance_id,omitempty" jsonschema:"Instance whose menu to edit"`
	ItemID         string          `json:"item_id" jsonschema:"Menu item id (mitm_…) from get_menu."`
	Name           *string         `json:"name,omitempty" jsonschema:"New name."`
	Description    *string         `json:"description,omitempty" jsonschema:"New description (\"\" clears it)."`
	Price          *string         `json:"price,omitempty" jsonschema:"New price as written: \"45,90\". Only a price the user gave."`
	PriceCents     *int64          `json:"price_cents,omitempty" jsonschema:"New price in cents (alternative to price)."`
	Category       *string         `json:"category,omitempty" jsonschema:"Move to this category, by name (\"\" for no category)."`
	CategoryID     *string         `json:"category_id,omitempty" jsonschema:"Move to this category id (mcat_…; \"\" for no category)."`
	CreateCategory bool            `json:"create_category,omitempty" jsonschema:"Create the category named in category when it does not exist."`
	Available      *bool           `json:"available,omitempty" jsonschema:"false marks it sold out (esgotado)."`
	Tags           *[]string       `json:"tags,omitempty" jsonschema:"Replace the tags."`
	Options        *[]menuOptionIn `json:"options,omitempty" jsonschema:"Replace ALL option groups of the item (prefer add_menu_option_group / update_menu_option_group for one group)."`
}

type menuItemRefInput struct {
	InstanceID string `json:"instance_id,omitempty" jsonschema:"Instance whose menu to edit"`
	ItemID     string `json:"item_id" jsonschema:"Menu item id (mitm_…) from get_menu."`
}

type setMenuItemAvailableInput struct {
	InstanceID string `json:"instance_id,omitempty" jsonschema:"Instance whose menu to edit"`
	ItemID     string `json:"item_id" jsonschema:"Menu item id (mitm_…) from get_menu."`
	Available  bool   `json:"available" jsonschema:"true: available; false: sold out (esgotado)."`
}

type menuOptionGroupInput struct {
	InstanceID string         `json:"instance_id,omitempty" jsonschema:"Instance whose menu to edit"`
	ItemID     string         `json:"item_id" jsonschema:"Menu item id (mitm_…) from get_menu."`
	Name       string         `json:"name" jsonschema:"Option group name, e.g. \"Tamanho\"."`
	Required   bool           `json:"required,omitempty" jsonschema:"The customer must pick at least one choice."`
	Min        int            `json:"min,omitempty" jsonschema:"Minimum choices (default 0, or 1 when required)."`
	Max        int            `json:"max,omitempty" jsonschema:"Maximum choices (default 1)."`
	Choices    []menuChoiceIn `json:"choices" jsonschema:"The choices (1 to 20)."`
}

type updateMenuOptionGroupInput struct {
	InstanceID string         `json:"instance_id,omitempty" jsonschema:"Instance whose menu to edit"`
	ItemID     string         `json:"item_id" jsonschema:"Menu item id (mitm_…) from get_menu."`
	OptionID   string         `json:"option_id" jsonschema:"Option group id (mopt_…) from get_menu_item."`
	Name       string         `json:"name" jsonschema:"Option group name."`
	Required   bool           `json:"required,omitempty" jsonschema:"The customer must pick at least one choice."`
	Min        int            `json:"min,omitempty" jsonschema:"Minimum choices."`
	Max        int            `json:"max,omitempty" jsonschema:"Maximum choices (default 1)."`
	Choices    []menuChoiceIn `json:"choices" jsonschema:"All the choices (they replace the current ones)."`
}

type menuOptionRefInput struct {
	InstanceID string `json:"instance_id,omitempty" jsonschema:"Instance whose menu to edit"`
	ItemID     string `json:"item_id" jsonschema:"Menu item id (mitm_…) from get_menu."`
	OptionID   string `json:"option_id" jsonschema:"Option group id (mopt_…) from get_menu_item."`
}

type setMenuItemImageInput struct {
	InstanceID string `json:"instance_id,omitempty" jsonschema:"Instance whose menu to edit"`
	ItemID     string `json:"item_id" jsonschema:"Menu item id (mitm_…) from get_menu."`
	URL        string `json:"url" jsonschema:"Public http(s) URL of the photo (JPEG, PNG or WebP up to 8 MB); private and local addresses are refused."`
}

type reorderMenuInput struct {
	InstanceID string   `json:"instance_id,omitempty" jsonschema:"Instance whose menu to edit"`
	Categories []string `json:"categories,omitempty" jsonschema:"Category ids (mcat_…) in their new order. Use this or items."`
	Items      []string `json:"items,omitempty" jsonschema:"Item ids (mitm_…) in their new order; they are all put in category_id."`
	CategoryID *string  `json:"category_id,omitempty" jsonschema:"With items: the category they go in (\"\" for no category). Required with items."`
}

type importRowIn struct {
	Category    string         `json:"category,omitempty" jsonschema:"Category name; created when it does not exist (\"\" for no category)."`
	Name        string         `json:"name" jsonschema:"Item name."`
	Description string         `json:"description,omitempty" jsonschema:"Description."`
	Price       string         `json:"price,omitempty" jsonschema:"Price exactly as the user wrote it: \"45,90\", \"R$ 1.234,50\", \"45\". Never invent or estimate one: leave it empty when the text has none (the row gets a warning)."`
	PriceCents  int64          `json:"price_cents,omitempty" jsonschema:"Price in cents (alternative to price)."`
	Available   *bool          `json:"available,omitempty" jsonschema:"false when the text marks it sold out (esgotado). Default true."`
	Tags        []string       `json:"tags,omitempty" jsonschema:"Short tags (up to 10)."`
	ImageURL    string         `json:"image_url,omitempty" jsonschema:"Optional public http(s) photo URL that appears in the user's data. Never make one up."`
	Options     []menuOptionIn `json:"options,omitempty" jsonschema:"Option groups, e.g. Tamanho: P, M (+5,00), G (+10,00)."`
}

type importMenuRowsInput struct {
	InstanceID  string        `json:"instance_id,omitempty" jsonschema:"Instance whose menu to build"`
	Rows        []importRowIn `json:"rows" jsonschema:"The menu items, one per row, in the order they should appear."`
	DryRun      *bool         `json:"dry_run,omitempty" jsonschema:"true (default): only validate and preview, nothing is saved. false: save, only after the user confirmed the preview."`
	Replace     bool          `json:"replace,omitempty" jsonschema:"DESTRUCTIVE: empty the whole menu (items, categories and photos) before saving. Only when the user explicitly asked to replace the menu. Default false (add to it)."`
	SkipInvalid bool          `json:"skip_invalid,omitempty" jsonschema:"Save the valid rows and skip the ones with errors (default false: any error refuses the save). Only when the user agreed to leave those rows out."`
}

type importRowOut struct {
	Line        int           `json:"line"`
	Category    string        `json:"category"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Price       string        `json:"price"`
	PriceCents  int64         `json:"price_cents"`
	Available   bool          `json:"available"`
	Tags        []string      `json:"tags,omitempty"`
	ImageURL    string        `json:"image_url,omitempty"`
	Options     []menu.Option `json:"options,omitempty"`
	Errors      []string      `json:"errors,omitempty"`
	Warnings    []string      `json:"warnings,omitempty"`
}

type importMenuRowsOutput struct {
	DryRun            bool           `json:"dry_run"`
	Rows              []importRowOut `json:"rows"`
	Errors            []string       `json:"errors"`
	Valid             int            `json:"valid"`
	Invalid           int            `json:"invalid"`
	ItemsInMenu       int            `json:"items_in_menu"`
	MaxItems          int            `json:"max_items"`
	CategoriesCreated int            `json:"categories_created"`
	ItemsCreated      int            `json:"items_created"`
	ImagesSaved       int            `json:"images_saved"`
	ImageErrors       []string       `json:"image_errors,omitempty"`
	ImagesPending     int            `json:"images_pending,omitempty"`
	Note              string         `json:"note"`
}

type menuOptionOut struct {
	menu.Option
	ItemID string `json:"item_id"`
}

type menuDeleted struct {
	Deleted bool   `json:"deleted"`
	Note    string `json:"note"`
}

type menuReordered struct {
	Reordered int    `json:"reordered"`
	Note      string `json:"note"`
}

// menuItemFull is an item as the editing tools return it (with its ids).
type menuItemFull struct {
	menuItemOut
	CategoryID string   `json:"category_id"`
	Tags       []string `json:"tags,omitempty"`
	Note       string   `json:"note,omitempty"`
}

func itemFull(v service.MenuItemView) menuItemFull {
	o := itemOut(v)
	o.Options = v.Options // with their ids, for the option tools
	return menuItemFull{menuItemOut: o, CategoryID: v.CategoryID, Tags: v.Tags}
}

// priceJSON turns a price text into the API's "price" field.
func priceJSON(p string) json.RawMessage {
	b, _ := json.Marshal(strings.TrimSpace(p))
	return b
}

func normName(s string) string { return strings.ToLower(menu.CleanLine(s)) }

// resolveCategory finds a category by name (creating it when asked) and returns its id.
func (t *tools) resolveCategory(ctx context.Context, mb MenuBackend, mw MenuWriter, id, name string, create bool) (string, error) {
	want := normName(name)
	if want == "" {
		return "", nil
	}
	v, err := mb.GetMenu(ctx, id)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(v.Categories))
	for _, c := range v.Categories {
		if normName(c.Name) == want {
			return c.ID, nil
		}
		names = append(names, c.Name)
	}
	if !create {
		have := "the menu has no categories yet"
		if len(names) > 0 {
			have = "existing: " + strings.Join(names, ", ")
		}
		return "", apierr.Validation(fmt.Sprintf("category %q does not exist (%s); pass create_category=true to create it, or use one of them", menu.CleanLine(name), have))
	}
	c, err := mw.CreateMenuCategory(ctx, id, name)
	if err != nil {
		return "", err
	}
	return c.ID, nil
}

const (
	menuEditNote = "Saved in LiteZap's own menu (Cardápio), never in WhatsApp/Meta. Customers only see it when it is sent (send_menu) " +
		"or when the attendant answers from it."
	menuOnlyWhenAsked = " Only when the user explicitly asks, for the one they identified: confirm with the user first; it cannot be undone."
)

// registerMenuWrite adds the menu editing tools (assistant only: never given to agents).
func (t *tools) registerMenuWrite(s *sdk.Server) {
	mb, ok := t.b.(MenuBackend)
	if !ok {
		return
	}
	mw, ok := t.b.(MenuWriter)
	if !ok {
		return
	}
	instID := func(p *jsonschema.Schema) { p.Description = instanceIDDoc }
	write := &sdk.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)}
	idem := &sdk.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)}
	destructive := &sdk.ToolAnnotations{DestructiveHint: ptr(true), IdempotentHint: true, OpenWorldHint: ptr(false)}
	choicesSchema := func(p *jsonschema.Schema) { items(1, store.MaxMenuChoices)(p) }

	addAssistant(t, s, &sdk.Tool{
		Name:  "import_menu_rows",
		Title: "Build the menu from rows (preview first)",
		Description: "The main tool to build this number's menu (Cardápio) from what the user gives you: pasted text, a photo of a printed menu " +
			"(read it yourself), a spreadsheet or CSV. Steps: 1) turn the user's material into rows (category, name, description, price exactly as written, " +
			"options such as Tamanho or Borda, sold-out items with available=false); 2) ALWAYS call this tool first with dry_run=true (the default): nothing is saved, " +
			"it returns the parsed rows with errors and warnings; 3) show that preview to the user as a table (category, item, price, options) and point out " +
			"errors, warnings and anything you were unsure about; 4) only after the user confirms, call it again with the same rows and dry_run=false. " +
			"Never invent prices, items or photo URLs: leave a missing price empty and ask the user. Saving adds to the menu (items already there are " +
			"flagged in the preview: saving again duplicates them); replace=true empties the menu first and is destructive: only when the user asks to replace it. " +
			fmt.Sprintf("Up to %d items and %d categories per menu. ", store.MaxMenuItems, store.MaxMenuCategories) + menuEditNote,
		InputSchema: schema[importMenuRowsInput](map[string]func(*jsonschema.Schema){
			"instance_id": instID, "rows": items(1, store.MaxMenuItems), "dry_run": boolDefault(true),
			"replace": boolDefault(false), "skip_invalid": boolDefault(false),
		}),
		Annotations: &sdk.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)},
	}, func(ctx context.Context, in importMenuRowsInput) (*sdk.CallToolResult, importMenuRowsOutput, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, importMenuRowsOutput{}, err
		}
		return t.importMenuRows(ctx, mb, mw, id, in)
	})

	addAssistant(t, s, &sdk.Tool{
		Name:  "create_menu_category",
		Title: "Create a menu category",
		Description: "Creates a category of this number's menu (Cardápio), e.g. \"Pizzas\" or \"Bebidas\", at the end. Only when the user asked for it " +
			fmt.Sprintf("(import_menu_rows creates categories by itself). Up to %d categories. ", store.MaxMenuCategories) + menuEditNote,
		InputSchema: schema[menuCategoryNameInput](map[string]func(*jsonschema.Schema){"instance_id": instID}),
		Annotations: write,
	}, func(ctx context.Context, in menuCategoryNameInput) (*sdk.CallToolResult, menuCategoryResult, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, menuCategoryResult{}, err
		}
		c, err := mw.CreateMenuCategory(ctx, id, in.Name)
		if err != nil {
			return nil, menuCategoryResult{}, err
		}
		return nil, menuCategoryResult{ID: c.ID, Name: c.Name, Note: "Category created."}, nil
	})

	addAssistant(t, s, &sdk.Tool{
		Name:        "update_menu_category",
		Title:       "Rename a menu category",
		Description: "Renames a category of this number's menu (Cardápio). Only when the user asked for it. " + menuEditNote,
		InputSchema: schema[menuCategoryRenameInput](map[string]func(*jsonschema.Schema){"instance_id": instID}),
		Annotations: idem,
	}, func(ctx context.Context, in menuCategoryRenameInput) (*sdk.CallToolResult, menuCategoryResult, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, menuCategoryResult{}, err
		}
		c, err := mw.RenameMenuCategory(ctx, id, strings.TrimSpace(in.CategoryID), in.Name)
		if err != nil {
			return nil, menuCategoryResult{}, err
		}
		return nil, menuCategoryResult{ID: c.ID, Name: c.Name, Note: "Category renamed."}, nil
	})

	addAssistant(t, s, &sdk.Tool{
		Name:  "delete_menu_category",
		Title: "Delete a menu category",
		Description: "Deletes a category of this number's menu (Cardápio); its items stay in the menu, without category (\"Outros\")." +
			menuOnlyWhenAsked,
		InputSchema: schema[menuCategoryRefInput](map[string]func(*jsonschema.Schema){"instance_id": instID}),
		Annotations: destructive,
	}, func(ctx context.Context, in menuCategoryRefInput) (*sdk.CallToolResult, menuDeleted, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, menuDeleted{}, err
		}
		if err := mw.DeleteMenuCategory(ctx, id, strings.TrimSpace(in.CategoryID)); err != nil {
			return nil, menuDeleted{}, err
		}
		return nil, menuDeleted{Deleted: true, Note: "Category deleted; its items are now without category."}, nil
	})

	addAssistant(t, s, &sdk.Tool{
		Name:  "create_menu_item",
		Title: "Add one menu item",
		Description: "Adds one item to this number's menu (Cardápio): name, description, price (as the user wrote it, \"45,90\", or price_cents), " +
			"category by name (create_category=true creates it when missing) or by id, availability, tags and option groups. For several items use " +
			"import_menu_rows (preview first). Never invent a price: ask the user. " + fmt.Sprintf("Up to %d items per menu. ", store.MaxMenuItems) + menuEditNote,
		InputSchema: schema[createMenuItemInput](map[string]func(*jsonschema.Schema){
			"instance_id": instID, "available": boolDefault(true), "create_category": boolDefault(false),
		}),
		Annotations: write,
	}, func(ctx context.Context, in createMenuItemInput) (*sdk.CallToolResult, menuItemFull, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, menuItemFull{}, err
		}
		si := service.MenuItemInput{Name: &in.Name, Available: in.Available, ImageURL: strings.TrimSpace(in.ImageURL)}
		if in.Description != "" {
			si.Description = &in.Description
		}
		if in.Price != "" && in.PriceCents != 0 {
			return nil, menuItemFull{}, apierr.Validation("send price or price_cents, not both")
		}
		if in.Price != "" {
			si.Price = priceJSON(in.Price)
		} else if in.PriceCents != 0 {
			si.PriceCents = &in.PriceCents
		}
		if in.Tags != nil {
			si.Tags = &in.Tags
		}
		if len(in.Options) > 0 {
			opts, err := optionsOf(in.Options)
			if err != nil {
				return nil, menuItemFull{}, err
			}
			si.Options = &opts
		}
		catID := strings.TrimSpace(in.CategoryID)
		if catID != "" && strings.TrimSpace(in.Category) != "" {
			return nil, menuItemFull{}, apierr.Validation("send category (a name) or category_id, not both")
		}
		if catID == "" {
			if catID, err = t.resolveCategory(ctx, mb, mw, id, in.Category, in.CreateCategory); err != nil {
				return nil, menuItemFull{}, err
			}
		}
		si.CategoryID = &catID
		v, err := mw.CreateMenuItem(ctx, id, si)
		if err != nil {
			return nil, menuItemFull{}, err
		}
		out := itemFull(v)
		out.Note = "Item created. " + menuEditNote
		if v.PriceCents == 0 {
			out.Note = "Item created without a price (it shows no price): confirm the price with the user. " + menuEditNote
		}
		return nil, out, nil
	})

	addAssistant(t, s, &sdk.Tool{
		Name:  "update_menu_item",
		Title: "Edit a menu item",
		Description: "Changes fields of one item of this number's menu (Cardápio); fields left out stay as they are. Get item_id from get_menu. " +
			"options replaces all of the item's option groups. Only change what the user asked for, and never invent a price. " + menuEditNote,
		InputSchema: schema[updateMenuItemInput](map[string]func(*jsonschema.Schema){"instance_id": instID, "create_category": boolDefault(false)}),
		Annotations: idem,
	}, func(ctx context.Context, in updateMenuItemInput) (*sdk.CallToolResult, menuItemFull, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, menuItemFull{}, err
		}
		si := service.MenuItemInput{Name: in.Name, Description: in.Description, PriceCents: in.PriceCents, Available: in.Available, Tags: in.Tags}
		if in.Price != nil {
			if in.PriceCents != nil {
				return nil, menuItemFull{}, apierr.Validation("send price or price_cents, not both")
			}
			si.Price = priceJSON(*in.Price)
		}
		if in.Options != nil {
			opts, err := optionsOf(*in.Options)
			if err != nil {
				return nil, menuItemFull{}, err
			}
			si.Options = &opts
		}
		switch {
		case in.CategoryID != nil && in.Category != nil:
			return nil, menuItemFull{}, apierr.Validation("send category (a name) or category_id, not both")
		case in.CategoryID != nil:
			c := strings.TrimSpace(*in.CategoryID)
			si.CategoryID = &c
		case in.Category != nil:
			c, err := t.resolveCategory(ctx, mb, mw, id, *in.Category, in.CreateCategory)
			if err != nil {
				return nil, menuItemFull{}, err
			}
			si.CategoryID = &c
		}
		v, err := mw.UpdateMenuItem(ctx, id, strings.TrimSpace(in.ItemID), si)
		if err != nil {
			return nil, menuItemFull{}, err
		}
		out := itemFull(v)
		out.Note = "Item saved. " + menuEditNote
		return nil, out, nil
	})

	addAssistant(t, s, &sdk.Tool{
		Name:        "delete_menu_item",
		Title:       "Delete a menu item",
		Description: "Deletes one item (and its photo) from this number's menu (Cardápio). To mark it sold out use set_menu_item_available instead." + menuOnlyWhenAsked,
		InputSchema: schema[menuItemRefInput](map[string]func(*jsonschema.Schema){"instance_id": instID}),
		Annotations: destructive,
	}, func(ctx context.Context, in menuItemRefInput) (*sdk.CallToolResult, menuDeleted, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, menuDeleted{}, err
		}
		if err := mw.DeleteMenuItem(ctx, id, strings.TrimSpace(in.ItemID)); err != nil {
			return nil, menuDeleted{}, err
		}
		return nil, menuDeleted{Deleted: true, Note: "Item deleted from the menu."}, nil
	})

	addAssistant(t, s, &sdk.Tool{
		Name:  "set_menu_item_available",
		Title: "Mark a menu item available or sold out",
		Description: "Marks one item of this number's menu (Cardápio) as available, or sold out (esgotado: available=false; it is left out of send_menu " +
			"and the attendant does not offer it). Only when the user asks. " + menuEditNote,
		InputSchema: schema[setMenuItemAvailableInput](map[string]func(*jsonschema.Schema){"instance_id": instID}),
		Annotations: idem,
	}, func(ctx context.Context, in setMenuItemAvailableInput) (*sdk.CallToolResult, menuItemFull, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, menuItemFull{}, err
		}
		v, err := mw.SetMenuItemAvailable(ctx, id, strings.TrimSpace(in.ItemID), in.Available)
		if err != nil {
			return nil, menuItemFull{}, err
		}
		out := itemFull(v)
		out.Note = "Marked available."
		if !v.Available {
			out.Note = "Marked sold out (esgotado)."
		}
		return nil, out, nil
	})

	addAssistant(t, s, &sdk.Tool{
		Name:  "add_menu_option_group",
		Title: "Add an option group to a menu item",
		Description: "Adds an option group to one item of this number's menu (Cardápio), e.g. Tamanho (P, M +5,00, G +10,00) or Borda (Catupiry +8,00). " +
			fmt.Sprintf("Up to %d groups per item and %d choices per group. Price differences only as the user gave them. ", store.MaxMenuOptions, store.MaxMenuChoices) + menuEditNote,
		InputSchema: schema[menuOptionGroupInput](map[string]func(*jsonschema.Schema){"instance_id": instID, "choices": choicesSchema}),
		Annotations: write,
	}, func(ctx context.Context, in menuOptionGroupInput) (*sdk.CallToolResult, menuOptionOut, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, menuOptionOut{}, err
		}
		o, err := menuOptionIn{Name: in.Name, Required: in.Required, Min: in.Min, Max: in.Max, Choices: in.Choices}.option()
		if err != nil {
			return nil, menuOptionOut{}, err
		}
		item := strings.TrimSpace(in.ItemID)
		saved, err := mw.AddMenuOption(ctx, id, item, o)
		if err != nil {
			return nil, menuOptionOut{}, err
		}
		return nil, menuOptionOut{Option: saved, ItemID: item}, nil
	})

	addAssistant(t, s, &sdk.Tool{
		Name:  "update_menu_option_group",
		Title: "Edit an option group of a menu item",
		Description: "Replaces one option group of a menu item (name, required, min, max and all its choices). Get option_id from get_menu_item. " +
			"Only when the user asked for the change. " + menuEditNote,
		InputSchema: schema[updateMenuOptionGroupInput](map[string]func(*jsonschema.Schema){"instance_id": instID, "choices": choicesSchema}),
		Annotations: idem,
	}, func(ctx context.Context, in updateMenuOptionGroupInput) (*sdk.CallToolResult, menuOptionOut, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, menuOptionOut{}, err
		}
		o, err := menuOptionIn{Name: in.Name, Required: in.Required, Min: in.Min, Max: in.Max, Choices: in.Choices}.option()
		if err != nil {
			return nil, menuOptionOut{}, err
		}
		item := strings.TrimSpace(in.ItemID)
		saved, err := mw.UpdateMenuOption(ctx, id, item, strings.TrimSpace(in.OptionID), o)
		if err != nil {
			return nil, menuOptionOut{}, err
		}
		return nil, menuOptionOut{Option: saved, ItemID: item}, nil
	})

	addAssistant(t, s, &sdk.Tool{
		Name:        "delete_menu_option_group",
		Title:       "Delete an option group of a menu item",
		Description: "Deletes one option group (and its choices) from a menu item. Get option_id from get_menu_item." + menuOnlyWhenAsked,
		InputSchema: schema[menuOptionRefInput](map[string]func(*jsonschema.Schema){"instance_id": instID}),
		Annotations: destructive,
	}, func(ctx context.Context, in menuOptionRefInput) (*sdk.CallToolResult, menuDeleted, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, menuDeleted{}, err
		}
		if err := mw.DeleteMenuOption(ctx, id, strings.TrimSpace(in.ItemID), strings.TrimSpace(in.OptionID)); err != nil {
			return nil, menuDeleted{}, err
		}
		return nil, menuDeleted{Deleted: true, Note: "Option group deleted."}, nil
	})

	addAssistant(t, s, &sdk.Tool{
		Name:  "set_menu_item_image_url",
		Title: "Set a menu item's photo from a URL",
		Description: "Downloads a photo from a public http(s) URL and sets it as one menu item's photo (resized to JPEG; up to 8 MB; private and " +
			"local addresses are refused). It replaces the current photo. Only URLs the user gave you: never make one up. " + menuEditNote,
		InputSchema: schema[setMenuItemImageInput](map[string]func(*jsonschema.Schema){"instance_id": instID}),
		Annotations: &sdk.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(true)},
	}, func(ctx context.Context, in setMenuItemImageInput) (*sdk.CallToolResult, menuItemFull, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, menuItemFull{}, err
		}
		u := strings.TrimSpace(in.URL)
		if u == "" || len(u) > menu.MaxImageURLLen {
			return nil, menuItemFull{}, apierr.Validation("url is required (a public http(s) address)")
		}
		v, err := mw.SetMenuItemImageURL(ctx, id, strings.TrimSpace(in.ItemID), u)
		if err != nil {
			return nil, menuItemFull{}, err
		}
		out := itemFull(v)
		out.Note = "Photo saved."
		return nil, out, nil
	})

	addAssistant(t, s, &sdk.Tool{
		Name:  "reorder_menu",
		Title: "Reorder the menu",
		Description: "Sets the order of this number's menu (Cardápio): either categories (category ids in their new order) or items (item ids in their new " +
			"order, all placed in category_id; \"\" for no category). Get the ids from get_menu. Only when the user asks. " + menuEditNote,
		InputSchema: schema[reorderMenuInput](map[string]func(*jsonschema.Schema){"instance_id": instID}),
		Annotations: idem,
	}, func(ctx context.Context, in reorderMenuInput) (*sdk.CallToolResult, menuReordered, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, menuReordered{}, err
		}
		switch {
		case len(in.Categories) > 0 && len(in.Items) > 0:
			return nil, menuReordered{}, apierr.Validation("send categories or items, not both")
		case len(in.Categories) > 0:
			if in.CategoryID != nil {
				return nil, menuReordered{}, apierr.Validation("category_id goes with items, not categories")
			}
			if err := mw.ReorderMenuCategories(ctx, id, in.Categories); err != nil {
				return nil, menuReordered{}, err
			}
			return nil, menuReordered{Reordered: len(in.Categories), Note: "Categories reordered."}, nil
		case len(in.Items) > 0:
			if in.CategoryID == nil {
				return nil, menuReordered{}, apierr.Validation(`category_id is required with items ("" for items without category)`)
			}
			c := strings.TrimSpace(*in.CategoryID)
			if err := mw.ReorderMenuItems(ctx, id, service.ReorderInput{IDs: in.Items, CategoryID: &c}); err != nil {
				return nil, menuReordered{}, err
			}
			return nil, menuReordered{Reordered: len(in.Items), Note: "Items reordered."}, nil
		}
		return nil, menuReordered{}, apierr.Validation("send categories or items (ids in their new order)")
	})
}

// maxListedProblems bounds the problems a refused save lists.
const maxListedProblems = 20

// importMenuRows previews or saves rows. The preview always runs first (also before a
// save), so a save with errors is refused with the problems listed, the same way over
// Local and REST, and the item limit is checked against the current menu.
func (t *tools) importMenuRows(ctx context.Context, mb MenuBackend, mw MenuWriter, id string, in importMenuRowsInput) (*sdk.CallToolResult, importMenuRowsOutput, error) {
	dry := in.DryRun == nil || *in.DryRun
	if len(in.Rows) == 0 || len(in.Rows) > store.MaxMenuItems {
		return nil, importMenuRowsOutput{}, apierr.Validation(fmt.Sprintf("rows lists 1 to %d items", store.MaxMenuItems))
	}
	rows := make([]menu.Row, 0, len(in.Rows))
	for i, r := range in.Rows {
		opts, err := optionsOf(r.Options)
		if err != nil {
			return nil, importMenuRowsOutput{}, apierr.Validation(fmt.Sprintf("row %d (%s): %s", i+1, menu.CleanLine(r.Name), errText(err)))
		}
		row := menu.Row{Category: r.Category, Name: r.Name, Description: r.Description, Price: r.Price, PriceCents: r.PriceCents,
			Available: r.Available == nil || *r.Available, Tags: r.Tags, ImageURL: r.ImageURL, Options: opts}
		rows = append(rows, row)
	}
	cur, err := mb.GetMenu(ctx, id)
	if err != nil {
		return nil, importMenuRowsOutput{}, err
	}
	pv, err := mw.ImportMenuRows(ctx, id, service.MenuRowsInput{Rows: rows, MenuImportOptions: service.MenuImportOptions{DryRun: true}})
	if err != nil {
		return nil, importMenuRowsOutput{}, err
	}
	out := importOut(pv, cur, in.Replace)
	if dry {
		out.DryRun = true
		out.Note = "PREVIEW ONLY: nothing was saved. Show these rows to the user as a table (category, item, price, options, availability), " +
			"point out the errors and warnings, and ask them to confirm or correct. Only after they confirm, call import_menu_rows again " +
			"with the same rows and dry_run=false."
		if out.Invalid > 0 {
			out.Note += fmt.Sprintf(" %d rows have errors: fix them with the user, or save the others with skip_invalid=true if they agree.", out.Invalid)
		}
		return nil, out, nil
	}
	if out.Invalid > 0 && !in.SkipInvalid {
		return nil, importMenuRowsOutput{}, apierr.Validation(fmt.Sprintf("nothing saved: %d rows have errors (%s). Fix them with the user, "+
			"or send skip_invalid=true to save only the valid rows", out.Invalid, problems(out.Rows)))
	}
	if len(out.Errors) > 0 {
		return nil, importMenuRowsOutput{}, apierr.New(http.StatusConflict, "menu_limit", "nothing saved: "+strings.Join(out.Errors, "; "), false)
	}
	v, err := mw.ImportMenuRows(ctx, id, service.MenuRowsInput{Rows: rows, MenuImportOptions: service.MenuImportOptions{Replace: in.Replace, SkipInvalid: in.SkipInvalid}})
	if err != nil {
		return nil, importMenuRowsOutput{}, err
	}
	saved := importOut(v, cur, in.Replace)
	saved.ItemsInMenu = out.ItemsInMenu + v.ItemsCreated
	saved.Note = fmt.Sprintf("Saved: %d items and %d new categories. Do not call it again with these rows (it would duplicate them). ", v.ItemsCreated, v.CategoriesCreated) +
		"Offer to show the menu (get_menu) or to send it to a chat the user names (send_menu). " + menuEditNote
	if v.ImagesPending > 0 {
		saved.Note += fmt.Sprintf(" %d photos were not fetched in this call: set them with set_menu_item_image_url.", v.ImagesPending)
	}
	return nil, saved, nil
}

// importOut is a preview or result as the tool shows it, with the rows already in the
// menu flagged and the item limit checked.
func importOut(v service.MenuImportView, cur service.MenuView, replace bool) importMenuRowsOutput {
	have := map[string]bool{}
	if !replace {
		key := func(cat, name string) string { return normName(cat) + "\x00" + normName(name) }
		for _, c := range cur.Categories {
			for _, it := range c.Items {
				have[key(c.Name, it.Name)] = true
			}
		}
		for _, it := range cur.Uncategorized {
			have[key("", it.Name)] = true
		}
		for i := range v.Rows {
			if have[key(v.Rows[i].Category, v.Rows[i].Name)] {
				v.Rows[i].Warnings = append(v.Rows[i].Warnings, "já existe no cardápio nesta categoria: salvar de novo cria um item repetido")
			}
		}
	}
	out := importMenuRowsOutput{DryRun: v.DryRun, Rows: make([]importRowOut, 0, len(v.Rows)), Errors: append([]string{}, v.Errors...),
		Valid: v.Valid, Invalid: v.Invalid, ItemsInMenu: cur.ItemCount, MaxItems: store.MaxMenuItems,
		CategoriesCreated: v.CategoriesCreated, ItemsCreated: v.ItemsCreated, ImagesSaved: v.ImagesSaved, ImageErrors: v.ImageErrors,
		ImagesPending: v.ImagesPending}
	if replace {
		out.ItemsInMenu = 0
	}
	if out.ItemsInMenu+v.Valid > store.MaxMenuItems {
		out.Errors = append(out.Errors, fmt.Sprintf("the menu has %d items and these rows add %d: the limit is %d items",
			out.ItemsInMenu, v.Valid, store.MaxMenuItems))
	}
	for _, r := range v.Rows {
		o := importRowOut{Line: r.Line, Category: r.Category, Name: r.Name, Description: r.Description, Price: r.PriceText,
			PriceCents: r.PriceCents, Available: r.Available, Tags: r.Tags, ImageURL: r.ImageURL, Errors: r.Errors, Warnings: r.Warnings}
		if len(r.Options) > 0 {
			o.Options = r.Options
		}
		out.Rows = append(out.Rows, o)
	}
	return out
}

// problems lists the rows with errors ("3 Calabresa: ...").
func problems(rows []importRowOut) string {
	var p []string
	for _, r := range rows {
		if len(r.Errors) == 0 {
			continue
		}
		if len(p) == maxListedProblems {
			p = append(p, "…")
			break
		}
		p = append(p, fmt.Sprintf("row %d %q: %s", r.Line, r.Name, strings.Join(r.Errors, ", ")))
	}
	return strings.Join(p, "; ")
}

func errText(err error) string {
	if ae, ok := err.(*apierr.Error); ok {
		return ae.Message
	}
	return err.Error()
}

// addAssistant registers a tool for the user's own assistant only: MCP serves it, but
// it is flagged AssistantOnly, so AgentCatalog (the LiteZap agents' table) leaves it out.
func addAssistant[In, Out any](t *tools, s *sdk.Server, tool *sdk.Tool, h func(context.Context, In) (*sdk.CallToolResult, Out, error)) {
	add(t, s, tool, h)
	t.defs[len(t.defs)-1].AssistantOnly = true
}
