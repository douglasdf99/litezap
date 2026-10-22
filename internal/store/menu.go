package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/secure"
)

// Cardápio (LiteZap's own catalog): categories, items and option groups per instance.
// The service validates the content; the store enforces the counts in the same
// transaction as the insert, so concurrent creations cannot pass the limits.

// Limits per instance (and per item).
const (
	MaxMenuCategories = 50
	MaxMenuItems      = 500
	MaxMenuOptions    = 10 // option groups per item
	MaxMenuChoices    = 20 // choices per option group
	MaxMenuImages     = 10 // photos per item
	// MaxMenuCollections and MaxCollectionItems bound the collections ("Promoções").
	MaxMenuCollections = 50
	MaxCollectionItems = 100
)

// ErrMenuLimit means a menu limit was reached.
var ErrMenuLimit = errors.New("menu limit reached")

// ErrMenuDuplicate means the item's "wa:" sku (a product copied from the WhatsApp
// catalog) is already in the menu.
var ErrMenuDuplicate = errors.New("menu item already copied")

type MenuCategory struct {
	ID        string
	Name      string
	Position  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

type MenuChoice struct {
	Name            string `json:"name"`
	PriceDeltaCents int64  `json:"price_delta_cents"`
}

type MenuOption struct {
	ID       string
	ItemID   string
	Name     string
	Min      int
	Max      int
	Required bool
	Position int
	Choices  []MenuChoice
}

type MenuItem struct {
	ID          string
	CategoryID  string
	Name        string
	Description string
	PriceCents  int64
	Currency    string
	// ImageID is the main photo (the first of Images); "" when the item has none.
	ImageID   string
	Available bool
	Position  int
	SKU       string
	Tags      []string
	Options   []MenuOption
	// SalePriceCents is the sale price (0: none), lower than PriceCents; SaleUntil is
	// its last day (YYYY-MM-DD, "" = no end).
	SalePriceCents int64
	SaleUntil      string
	LinkURL        string
	// Hidden items are never shown to customers, the attendant or the agents.
	Hidden bool
	// SourceRef marks a copy ("wa:<product id>"): an item is copied once.
	SourceRef string
	// Images are the photos in order (the first is the main one); CollectionIDs the
	// collections the item is in.
	Images        []MenuImage
	CollectionIDs []string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// MenuImage is one photo of an item.
type MenuImage struct {
	ID        string
	ItemID    string
	Position  int
	SourceURL string
}

type queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// ---- categories ----

func scanCategory(sc interface{ Scan(...any) error }) (MenuCategory, error) {
	var c MenuCategory
	var ca, ua string
	err := sc.Scan(&c.ID, &c.Name, &c.Position, &ca, &ua)
	c.CreatedAt, c.UpdatedAt = parseTS(ca), parseTS(ua)
	return c, err
}

const categoryCols = `id, name, position, created_at, updated_at`

// ListMenuCategories lists the categories in display order.
func (s *Store) ListMenuCategories(ctx context.Context, instanceID string) ([]MenuCategory, error) {
	return listCategories(ctx, s.db, instanceID)
}

func listCategories(ctx context.Context, q queryer, instanceID string) ([]MenuCategory, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+categoryCols+` FROM menu_categories WHERE instance_id = ?
		ORDER BY position, created_at, id`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MenuCategory{}
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetMenuCategory returns one category (ErrNotFound when unknown).
func (s *Store) GetMenuCategory(ctx context.Context, instanceID, id string) (MenuCategory, error) {
	c, err := scanCategory(s.db.QueryRowContext(ctx, `SELECT `+categoryCols+` FROM menu_categories WHERE instance_id = ? AND id = ?`,
		instanceID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MenuCategory{}, ErrNotFound
	}
	return c, err
}

// CreateMenuCategory adds a category at the end (ErrMenuLimit over MaxMenuCategories).
func (s *Store) CreateMenuCategory(ctx context.Context, instanceID, name string) (MenuCategory, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MenuCategory{}, err
	}
	defer tx.Rollback()
	c, err := createCategory(ctx, tx, instanceID, name)
	if err != nil {
		return MenuCategory{}, err
	}
	return c, tx.Commit()
}

func createCategory(ctx context.Context, tx *sql.Tx, instanceID, name string) (MenuCategory, error) {
	var n, pos int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(position) + 1, 0) FROM menu_categories WHERE instance_id = ?`,
		instanceID).Scan(&n, &pos); err != nil {
		return MenuCategory{}, err
	}
	if n >= MaxMenuCategories {
		return MenuCategory{}, ErrMenuLimit
	}
	ts := now()
	c := MenuCategory{ID: secure.NewID("mcat_"), Name: name, Position: pos, CreatedAt: parseTS(ts), UpdatedAt: parseTS(ts)}
	_, err := tx.ExecContext(ctx, `INSERT INTO menu_categories (id, instance_id, name, position, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, c.ID, instanceID, name, pos, ts, ts)
	return c, err
}

// RenameMenuCategory changes a category's name.
func (s *Store) RenameMenuCategory(ctx context.Context, instanceID, id, name string) error {
	return s.exec1(ctx, `UPDATE menu_categories SET name = ?, updated_at = ? WHERE instance_id = ? AND id = ?`,
		name, now(), instanceID, id)
}

// DeleteMenuCategory removes a category; its items stay, without category.
func (s *Store) DeleteMenuCategory(ctx context.Context, instanceID, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM menu_categories WHERE instance_id = ? AND id = ?`, instanceID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE menu_items SET category_id = '', updated_at = ? WHERE instance_id = ? AND category_id = ?`,
		now(), instanceID, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ReorderMenuCategories sets the positions of the listed categories (in that order);
// every id must be a category of the instance.
func (s *Store) ReorderMenuCategories(ctx context.Context, instanceID string, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		res, err := tx.ExecContext(ctx, `UPDATE menu_categories SET position = ? WHERE instance_id = ? AND id = ?`, i, instanceID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
	}
	return tx.Commit()
}

// ---- items ----

const itemCols = `id, category_id, name, description, price_cents, currency, image_id, available, position, sku, tags,
	sale_price_cents, sale_until, link_url, hidden, source_ref, created_at, updated_at`

func scanItem(sc interface{ Scan(...any) error }) (MenuItem, error) {
	var it MenuItem
	var tags, ca, ua string
	err := sc.Scan(&it.ID, &it.CategoryID, &it.Name, &it.Description, &it.PriceCents, &it.Currency, &it.ImageID, &it.Available,
		&it.Position, &it.SKU, &tags, &it.SalePriceCents, &it.SaleUntil, &it.LinkURL, &it.Hidden, &it.SourceRef, &ca, &ua)
	_ = json.Unmarshal([]byte(tags), &it.Tags)
	if it.Tags == nil {
		it.Tags = []string{}
	}
	it.CreatedAt, it.UpdatedAt = parseTS(ca), parseTS(ua)
	return it, err
}

// ListMenuItems lists every item with its options, in display order (category position,
// then item position; items without category last).
func (s *Store) ListMenuItems(ctx context.Context, instanceID string) ([]MenuItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixed("i.", itemCols)+` FROM menu_items i
		LEFT JOIN menu_categories c ON c.id = i.category_id AND c.instance_id = i.instance_id
		WHERE i.instance_id = ? ORDER BY c.id IS NULL, c.position, c.created_at, i.position, i.created_at, i.id`, instanceID)
	if err != nil {
		return nil, err
	}
	out := []MenuItem{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, it)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	opts, err := listOptions(ctx, s.db, instanceID, "")
	if err != nil {
		return nil, err
	}
	imgs, err := listImages(ctx, s.db, instanceID, "")
	if err != nil {
		return nil, err
	}
	cols, err := listMemberships(ctx, s.db, instanceID, "")
	if err != nil {
		return nil, err
	}
	for i := range out {
		attach(&out[i], opts, imgs, cols)
	}
	return out, nil
}

// attach sets an item's options, photos and collections (never nil).
func attach(it *MenuItem, opts map[string][]MenuOption, imgs map[string][]MenuImage, cols map[string][]string) {
	it.Options, it.Images, it.CollectionIDs = opts[it.ID], imgs[it.ID], cols[it.ID]
	if it.Options == nil {
		it.Options = []MenuOption{}
	}
	if it.Images == nil {
		it.Images = []MenuImage{}
	}
	if it.CollectionIDs == nil {
		it.CollectionIDs = []string{}
	}
}

func prefixed(p, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = p + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// GetMenuItem returns one item with its options (ErrNotFound when unknown).
func (s *Store) GetMenuItem(ctx context.Context, instanceID, id string) (MenuItem, error) {
	return getItem(ctx, s.db, instanceID, id)
}

func getItem(ctx context.Context, q queryer, instanceID, id string) (MenuItem, error) {
	it, err := scanItem(q.QueryRowContext(ctx, `SELECT `+itemCols+` FROM menu_items WHERE instance_id = ? AND id = ?`, instanceID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MenuItem{}, ErrNotFound
	}
	if err != nil {
		return MenuItem{}, err
	}
	opts, err := listOptions(ctx, q, instanceID, id)
	if err != nil {
		return MenuItem{}, err
	}
	imgs, err := listImages(ctx, q, instanceID, id)
	if err != nil {
		return MenuItem{}, err
	}
	cols, err := listMemberships(ctx, q, instanceID, id)
	if err != nil {
		return MenuItem{}, err
	}
	attach(&it, opts, imgs, cols)
	return it, nil
}

// MenuCounts returns the number of categories and items of an instance.
func (s *Store) MenuCounts(ctx context.Context, instanceID string) (int, int, error) {
	var c, i int
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM menu_categories WHERE instance_id = ?),
		(SELECT COUNT(*) FROM menu_items WHERE instance_id = ?)`, instanceID, instanceID).Scan(&c, &i)
	return c, i, err
}

func categoryExists(ctx context.Context, q queryer, instanceID, id string) (bool, error) {
	if id == "" {
		return true, nil
	}
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM menu_categories WHERE instance_id = ? AND id = ?`, instanceID, id).Scan(&n)
	return n > 0, err
}

// CreateMenuItem adds an item (with its options) at the end of its category. An unknown
// category is ErrNotFound; over MaxMenuItems, ErrMenuLimit.
func (s *Store) CreateMenuItem(ctx context.Context, instanceID string, it MenuItem) (MenuItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MenuItem{}, err
	}
	defer tx.Rollback()
	it, err = createItem(ctx, tx, instanceID, it)
	if err != nil {
		return MenuItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return MenuItem{}, err
	}
	return s.GetMenuItem(ctx, instanceID, it.ID)
}

func createItem(ctx context.Context, tx *sql.Tx, instanceID string, it MenuItem) (MenuItem, error) {
	ok, err := categoryExists(ctx, tx, instanceID, it.CategoryID)
	if err != nil {
		return MenuItem{}, err
	}
	if !ok {
		return MenuItem{}, ErrNotFound
	}
	var n, pos int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM menu_items WHERE instance_id = ?),
		(SELECT COALESCE(MAX(position) + 1, 0) FROM menu_items WHERE instance_id = ? AND category_id = ?)`,
		instanceID, instanceID, it.CategoryID).Scan(&n, &pos); err != nil {
		return MenuItem{}, err
	}
	if n >= MaxMenuItems {
		return MenuItem{}, ErrMenuLimit
	}
	ts := now()
	it.ID, it.Position = secure.NewID("mitm_"), pos
	if it.Currency == "" {
		it.Currency = "BRL"
	}
	if it.Tags == nil {
		it.Tags = []string{}
	}
	tags, _ := json.Marshal(it.Tags)
	// The photos are added afterwards (AddMenuItemImage): a new item starts without one.
	it.ImageID = ""
	ins, err := tx.ExecContext(ctx, `INSERT INTO menu_items (id, instance_id, category_id, name, description, price_cents, currency,
		image_id, available, position, sku, tags, sale_price_cents, sale_until, link_url, hidden, source_ref, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		it.ID, instanceID, it.CategoryID, it.Name, it.Description, it.PriceCents, it.Currency, it.ImageID, it.Available, pos,
		it.SKU, string(tags), it.SalePriceCents, it.SaleUntil, it.LinkURL, it.Hidden, it.SourceRef, ts, ts)
	if err != nil {
		return MenuItem{}, err
	}
	if n, _ := ins.RowsAffected(); n == 0 { // a "wa:" sku or source_ref already in the menu
		return MenuItem{}, ErrMenuDuplicate
	}
	if err := replaceOptions(ctx, tx, instanceID, it.ID, it.Options); err != nil {
		return MenuItem{}, err
	}
	for _, cid := range it.CollectionIDs {
		if err := addToCollection(ctx, tx, instanceID, cid, []string{it.ID}); err != nil {
			return MenuItem{}, err
		}
	}
	return it, nil
}

// UpdateMenuItem replaces an item's fields (not its photos, position or source_ref);
// options are replaced when it.Options is not nil, and the collections when
// it.CollectionIDs is not nil. Moving to another category puts it at the end.
func (s *Store) UpdateMenuItem(ctx context.Context, instanceID string, it MenuItem) (MenuItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MenuItem{}, err
	}
	defer tx.Rollback()
	cur, err := getItem(ctx, tx, instanceID, it.ID)
	if err != nil {
		return MenuItem{}, err
	}
	ok, err := categoryExists(ctx, tx, instanceID, it.CategoryID)
	if err != nil {
		return MenuItem{}, err
	}
	if !ok {
		return MenuItem{}, ErrNotFound
	}
	pos := cur.Position
	if cur.CategoryID != it.CategoryID {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position) + 1, 0) FROM menu_items WHERE instance_id = ? AND category_id = ?`,
			instanceID, it.CategoryID).Scan(&pos); err != nil {
			return MenuItem{}, err
		}
	}
	if it.Currency == "" {
		it.Currency = "BRL"
	}
	if it.Tags == nil {
		it.Tags = []string{}
	}
	tags, _ := json.Marshal(it.Tags)
	if _, err := tx.ExecContext(ctx, `UPDATE menu_items SET category_id = ?, name = ?, description = ?, price_cents = ?, currency = ?,
		available = ?, position = ?, sku = ?, tags = ?, sale_price_cents = ?, sale_until = ?, link_url = ?, hidden = ?, updated_at = ?
		WHERE instance_id = ? AND id = ?`,
		it.CategoryID, it.Name, it.Description, it.PriceCents, it.Currency, it.Available, pos, it.SKU, string(tags),
		it.SalePriceCents, it.SaleUntil, it.LinkURL, it.Hidden, now(), instanceID, it.ID); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return MenuItem{}, ErrMenuDuplicate
		}
		return MenuItem{}, err
	}
	if it.Options != nil {
		if err := replaceOptions(ctx, tx, instanceID, it.ID, it.Options); err != nil {
			return MenuItem{}, err
		}
	}
	if it.CollectionIDs != nil {
		if err := setItemCollections(ctx, tx, instanceID, it.ID, cur.CollectionIDs, it.CollectionIDs); err != nil {
			return MenuItem{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return MenuItem{}, err
	}
	return s.GetMenuItem(ctx, instanceID, it.ID)
}

// SetMenuItemHidden hides an item from customers, the attendant and the agents.
func (s *Store) SetMenuItemHidden(ctx context.Context, instanceID, id string, hidden bool) error {
	return s.exec1(ctx, `UPDATE menu_items SET hidden = ?, updated_at = ? WHERE instance_id = ? AND id = ?`,
		hidden, now(), instanceID, id)
}

// SetMenuItemAvailable toggles "esgotado".
func (s *Store) SetMenuItemAvailable(ctx context.Context, instanceID, id string, available bool) error {
	return s.exec1(ctx, `UPDATE menu_items SET available = ?, updated_at = ? WHERE instance_id = ? AND id = ?`,
		available, now(), instanceID, id)
}

// ReorderMenuItems puts the listed items in categoryID ("" = no category), in that order.
// Items listed from another category move into it.
func (s *Store) ReorderMenuItems(ctx context.Context, instanceID, categoryID string, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ok, err := categoryExists(ctx, tx, instanceID, categoryID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	ts := now()
	for i, id := range ids {
		res, err := tx.ExecContext(ctx, `UPDATE menu_items SET category_id = ?, position = ?, updated_at = ? WHERE instance_id = ? AND id = ?`,
			categoryID, i, ts, instanceID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
	}
	return tx.Commit()
}

// ---- options ----

func listOptions(ctx context.Context, q queryer, instanceID, itemID string) (map[string][]MenuOption, error) {
	query := `SELECT id, item_id, name, min_select, max_select, required, position, choices FROM menu_options WHERE instance_id = ?`
	args := []any{instanceID}
	if itemID != "" {
		query += ` AND item_id = ?`
		args = append(args, itemID)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY item_id, position, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]MenuOption{}
	for rows.Next() {
		var o MenuOption
		var choices string
		if err := rows.Scan(&o.ID, &o.ItemID, &o.Name, &o.Min, &o.Max, &o.Required, &o.Position, &choices); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(choices), &o.Choices)
		if o.Choices == nil {
			o.Choices = []MenuChoice{}
		}
		out[o.ItemID] = append(out[o.ItemID], o)
	}
	return out, rows.Err()
}

func insertOption(ctx context.Context, tx *sql.Tx, instanceID, itemID string, o MenuOption, pos int) (MenuOption, error) {
	if len(o.Choices) > MaxMenuChoices {
		return MenuOption{}, ErrMenuLimit
	}
	if o.Choices == nil {
		o.Choices = []MenuChoice{}
	}
	o.ID, o.ItemID, o.Position = secure.NewID("mopt_"), itemID, pos
	choices, _ := json.Marshal(o.Choices)
	_, err := tx.ExecContext(ctx, `INSERT INTO menu_options (id, instance_id, item_id, name, min_select, max_select, required, position, choices)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, o.ID, instanceID, itemID, o.Name, o.Min, o.Max, o.Required, pos, string(choices))
	return o, err
}

func replaceOptions(ctx context.Context, tx *sql.Tx, instanceID, itemID string, opts []MenuOption) error {
	if len(opts) > MaxMenuOptions {
		return ErrMenuLimit
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM menu_options WHERE instance_id = ? AND item_id = ?`, instanceID, itemID); err != nil {
		return err
	}
	for i, o := range opts {
		if _, err := insertOption(ctx, tx, instanceID, itemID, o, i); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) touchItem(ctx context.Context, tx *sql.Tx, instanceID, itemID string) error {
	res, err := tx.ExecContext(ctx, `UPDATE menu_items SET updated_at = ? WHERE instance_id = ? AND id = ?`, now(), instanceID, itemID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddMenuOption appends an option group to an item (ErrMenuLimit over MaxMenuOptions).
func (s *Store) AddMenuOption(ctx context.Context, instanceID, itemID string, o MenuOption) (MenuOption, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MenuOption{}, err
	}
	defer tx.Rollback()
	if err := s.touchItem(ctx, tx, instanceID, itemID); err != nil {
		return MenuOption{}, err
	}
	var n, pos int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(position) + 1, 0) FROM menu_options WHERE instance_id = ? AND item_id = ?`,
		instanceID, itemID).Scan(&n, &pos); err != nil {
		return MenuOption{}, err
	}
	if n >= MaxMenuOptions {
		return MenuOption{}, ErrMenuLimit
	}
	o, err = insertOption(ctx, tx, instanceID, itemID, o, pos)
	if err != nil {
		return MenuOption{}, err
	}
	return o, tx.Commit()
}

// UpdateMenuOption replaces an option group's fields and choices.
func (s *Store) UpdateMenuOption(ctx context.Context, instanceID, itemID string, o MenuOption) error {
	if len(o.Choices) > MaxMenuChoices {
		return ErrMenuLimit
	}
	if o.Choices == nil {
		o.Choices = []MenuChoice{}
	}
	choices, _ := json.Marshal(o.Choices)
	return s.optionChange(ctx, instanceID, itemID, `UPDATE menu_options SET name = ?, min_select = ?, max_select = ?, required = ?, choices = ?
		WHERE instance_id = ? AND item_id = ? AND id = ?`, o.Name, o.Min, o.Max, o.Required, string(choices), instanceID, itemID, o.ID)
}

// DeleteMenuOption removes an option group.
func (s *Store) DeleteMenuOption(ctx context.Context, instanceID, itemID, id string) error {
	return s.optionChange(ctx, instanceID, itemID, `DELETE FROM menu_options WHERE instance_id = ? AND item_id = ? AND id = ?`,
		instanceID, itemID, id)
}

// optionChange runs a statement on one option group and bumps its item's updated_at.
func (s *Store) optionChange(ctx context.Context, instanceID, itemID, q string, args ...any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := s.touchItem(ctx, tx, instanceID, itemID); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- import ----

// MenuImportItem is an item to import, with its category and collections by name.
type MenuImportItem struct {
	Category    string
	Collections []string
	Item        MenuItem
}

// MenuImportResult counts what an import created.
type MenuImportResult struct {
	CategoriesCreated  int
	CollectionsCreated int
	ItemsCreated       int
	// Skipped are items whose "wa:" sku was already in the menu.
	Skipped int
	ItemIDs []string // created items, in input order ("" for a skipped one)
	// RemovedImages are the image ids of the items a replace removed.
	RemovedImages []string
}

// ImportMenu adds items in one transaction, creating the categories they name (matched
// case-insensitively). With replace, the instance's menu is emptied first. Any limit or
// error rolls the whole import back.
func (s *Store) ImportMenu(ctx context.Context, instanceID string, replace bool, items []MenuImportItem) (MenuImportResult, error) {
	var res MenuImportResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	if replace {
		used, err := imageIDs(ctx, tx, instanceID)
		if err != nil {
			return res, err
		}
		for id := range used {
			res.RemovedImages = append(res.RemovedImages, id)
		}
		for _, q := range []string{`DELETE FROM menu_items WHERE instance_id = ?`, `DELETE FROM menu_categories WHERE instance_id = ?`,
			`DELETE FROM menu_collections WHERE instance_id = ?`} {
			if _, err := tx.ExecContext(ctx, q, instanceID); err != nil {
				return res, err
			}
		}
	}
	cats, err := listCategories(ctx, tx, instanceID)
	if err != nil {
		return res, err
	}
	byName := map[string]string{}
	for _, c := range cats {
		byName[strings.ToLower(strings.TrimSpace(c.Name))] = c.ID
	}
	colls, err := listCollections(ctx, tx, instanceID)
	if err != nil {
		return res, err
	}
	collByName := map[string]string{}
	for _, c := range colls {
		if _, ok := collByName[strings.ToLower(strings.TrimSpace(c.Name))]; !ok {
			collByName[strings.ToLower(strings.TrimSpace(c.Name))] = c.ID
		}
	}
	for _, in := range items {
		catID := ""
		if name := strings.TrimSpace(in.Category); name != "" {
			key := strings.ToLower(name)
			id, ok := byName[key]
			if !ok {
				c, err := createCategory(ctx, tx, instanceID, name)
				if err != nil {
					return MenuImportResult{}, err
				}
				id, byName[key] = c.ID, c.ID
				res.CategoriesCreated++
			}
			catID = id
		}
		it := in.Item
		it.CategoryID = catID
		it.CollectionIDs = nil
		for _, name := range in.Collections {
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" {
				continue
			}
			id, ok := collByName[key]
			if !ok {
				c, err := createCollection(ctx, tx, instanceID, MenuCollection{Name: strings.TrimSpace(name), Active: true})
				if err != nil {
					return MenuImportResult{}, err
				}
				id, collByName[key] = c.ID, c.ID
				res.CollectionsCreated++
			}
			if !slices.Contains(it.CollectionIDs, id) {
				it.CollectionIDs = append(it.CollectionIDs, id)
			}
		}
		created, err := createItem(ctx, tx, instanceID, it)
		if errors.Is(err, ErrMenuDuplicate) { // copied already (or by a concurrent copy)
			res.Skipped++
			res.ItemIDs = append(res.ItemIDs, "")
			continue
		}
		if err != nil {
			return MenuImportResult{}, err
		}
		res.ItemsCreated++
		res.ItemIDs = append(res.ItemIDs, created.ID)
	}
	return res, tx.Commit()
}

// MenuItemBySource finds an item by its source_ref ("wa:<product id>"; ErrNotFound
// when none).
func (s *Store) MenuItemBySource(ctx context.Context, instanceID, ref string) (MenuItem, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM menu_items WHERE instance_id = ? AND source_ref = ? LIMIT 1`,
		instanceID, ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return MenuItem{}, ErrNotFound
	}
	if err != nil {
		return MenuItem{}, err
	}
	return s.GetMenuItem(ctx, instanceID, id)
}

// MenuItemBySKU finds an item by its SKU (ErrNotFound when none).
func (s *Store) MenuItemBySKU(ctx context.Context, instanceID, sku string) (MenuItem, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM menu_items WHERE instance_id = ? AND sku = ? ORDER BY created_at LIMIT 1`,
		instanceID, sku).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return MenuItem{}, ErrNotFound
	}
	if err != nil {
		return MenuItem{}, err
	}
	return s.GetMenuItem(ctx, instanceID, id)
}
