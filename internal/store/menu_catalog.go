package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/douglasdf99/litezap/internal/secure"
)

// Cardápio, parity with the WhatsApp catalog: several photos per item, collections
// ("Promoções", many-to-many with items), bulk changes, duplicates and the order drafts
// built in a chat. As in menu.go, the limits are checked in the transaction that writes.

// ---- photos ----

func listImages(ctx context.Context, q queryer, instanceID, itemID string) (map[string][]MenuImage, error) {
	query := `SELECT id, item_id, position, source_url FROM menu_item_images WHERE instance_id = ?`
	args := []any{instanceID}
	if itemID != "" {
		query += ` AND item_id = ?`
		args = append(args, itemID)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY item_id, position, created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]MenuImage{}
	for rows.Next() {
		var im MenuImage
		if err := rows.Scan(&im.ID, &im.ItemID, &im.Position, &im.SourceURL); err != nil {
			return nil, err
		}
		out[im.ItemID] = append(out[im.ItemID], im)
	}
	return out, rows.Err()
}

// imageIDs lists every photo id of an instance (the photo table and the main photo
// column, which mirror each other).
func imageIDs(ctx context.Context, q queryer, instanceID string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM menu_item_images WHERE instance_id = ?
		UNION SELECT image_id FROM menu_items WHERE instance_id = ? AND image_id != ''`, instanceID, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// MenuImageIDs lists the image ids in use (to remove orphaned files).
func (s *Store) MenuImageIDs(ctx context.Context, instanceID string) (map[string]bool, error) {
	return imageIDs(ctx, s.db, instanceID)
}

// itemImageIDs lists the photo ids of one item, in order.
func itemImageIDs(ctx context.Context, q queryer, instanceID, itemID string) ([]string, error) {
	m, err := listImages(ctx, q, instanceID, itemID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, im := range m[itemID] {
		ids = append(ids, im.ID)
	}
	return ids, nil
}

// writeImageOrder renumbers an item's photos in the given order and points the item's
// main photo (image_id) at the first one.
func writeImageOrder(ctx context.Context, tx *sql.Tx, instanceID, itemID string, ids []string) error {
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE menu_item_images SET position = ? WHERE instance_id = ? AND item_id = ? AND id = ?`,
			i, instanceID, itemID, id); err != nil {
			return err
		}
	}
	main := ""
	if len(ids) > 0 {
		main = ids[0]
	}
	_, err := tx.ExecContext(ctx, `UPDATE menu_items SET image_id = ?, updated_at = ? WHERE instance_id = ? AND id = ?`,
		main, now(), instanceID, itemID)
	return err
}

func itemExists(ctx context.Context, q queryer, instanceID, itemID string) error {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM menu_items WHERE instance_id = ? AND id = ?`, instanceID, itemID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddMenuItemImage adds a photo (its file already written) at the end of the item's
// photos, or first (main) with main. ErrMenuLimit over MaxMenuImages photos.
func (s *Store) AddMenuItemImage(ctx context.Context, instanceID, itemID, imageID, sourceURL string, main bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := itemExists(ctx, tx, instanceID, itemID); err != nil {
		return err
	}
	ids, err := itemImageIDs(ctx, tx, instanceID, itemID)
	if err != nil {
		return err
	}
	if len(ids) >= MaxMenuImages {
		return ErrMenuLimit
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO menu_item_images (id, instance_id, item_id, position, source_url, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, imageID, instanceID, itemID, len(ids), sourceURL, now()); err != nil {
		return err
	}
	if main {
		ids = append([]string{imageID}, ids...)
	} else {
		ids = append(ids, imageID)
	}
	if err := writeImageOrder(ctx, tx, instanceID, itemID, ids); err != nil {
		return err
	}
	return tx.Commit()
}

// SetMenuItemImage replaces the item's main photo with imageID (the single-photo API):
// the previous main photo is removed and its id returned, so the caller can remove its
// file. With "", the main photo is removed and the next one becomes the main photo.
func (s *Store) SetMenuItemImage(ctx context.Context, instanceID, itemID, imageID, sourceURL string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := itemExists(ctx, tx, instanceID, itemID); err != nil {
		return "", err
	}
	ids, err := itemImageIDs(ctx, tx, instanceID, itemID)
	if err != nil {
		return "", err
	}
	prev := ""
	if len(ids) > 0 {
		prev = ids[0]
		if err := dropImage(ctx, tx, instanceID, prev); err != nil {
			return "", err
		}
		ids = ids[1:]
	}
	if imageID != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO menu_item_images (id, instance_id, item_id, position, source_url, created_at)
			VALUES (?, ?, ?, 0, ?, ?)`, imageID, instanceID, itemID, sourceURL, now()); err != nil {
			return "", err
		}
		ids = append([]string{imageID}, ids...)
	}
	if err := writeImageOrder(ctx, tx, instanceID, itemID, ids); err != nil {
		return "", err
	}
	return prev, tx.Commit()
}

// dropImage deletes a photo row and clears the collection covers that used it.
func dropImage(ctx context.Context, tx *sql.Tx, instanceID, imageID string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM menu_item_images WHERE instance_id = ? AND id = ?`, instanceID, imageID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE menu_collections SET cover_image_id = '', updated_at = ? WHERE instance_id = ? AND cover_image_id = ?`,
		now(), instanceID, imageID)
	return err
}

// RemoveMenuItemImage removes one photo of an item (ErrNotFound when it is not one).
func (s *Store) RemoveMenuItemImage(ctx context.Context, instanceID, itemID, imageID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ids, err := itemImageIDs(ctx, tx, instanceID, itemID)
	if err != nil {
		return err
	}
	i := slices.Index(ids, imageID)
	if i < 0 {
		return ErrNotFound
	}
	if err := dropImage(ctx, tx, instanceID, imageID); err != nil {
		return err
	}
	if err := writeImageOrder(ctx, tx, instanceID, itemID, slices.Delete(ids, i, i+1)); err != nil {
		return err
	}
	return tx.Commit()
}

// ReorderMenuItemImages sets the order of an item's photos: ids lists all of them (the
// first becomes the main photo).
func (s *Store) ReorderMenuItemImages(ctx context.Context, instanceID, itemID string, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := itemExists(ctx, tx, instanceID, itemID); err != nil {
		return err
	}
	cur, err := itemImageIDs(ctx, tx, instanceID, itemID)
	if err != nil {
		return err
	}
	if len(cur) != len(ids) {
		return ErrMenuOrder
	}
	for _, id := range ids {
		if !slices.Contains(cur, id) {
			return ErrMenuOrder
		}
	}
	if err := writeImageOrder(ctx, tx, instanceID, itemID, ids); err != nil {
		return err
	}
	return tx.Commit()
}

// SetMenuItemMainImage makes one photo of an item its main photo (the others keep
// their order).
func (s *Store) SetMenuItemMainImage(ctx context.Context, instanceID, itemID, imageID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ids, err := itemImageIDs(ctx, tx, instanceID, itemID)
	if err != nil {
		return err
	}
	i := slices.Index(ids, imageID)
	if i < 0 {
		return ErrNotFound
	}
	ids = append([]string{imageID}, slices.Delete(ids, i, i+1)...)
	if err := writeImageOrder(ctx, tx, instanceID, itemID, ids); err != nil {
		return err
	}
	return tx.Commit()
}

// ErrMenuOrder means a reorder did not list exactly the current members.
var ErrMenuOrder = errors.New("the ids must list every current member exactly once")

// DeleteMenuItem removes an item (with its options and photos) and returns its photo
// ids, so the caller can remove their files.
func (s *Store) DeleteMenuItem(ctx context.Context, instanceID, id string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	imgs, err := deleteItems(ctx, tx, instanceID, []string{id})
	if err != nil {
		return nil, err
	}
	return imgs, tx.Commit()
}

// DeleteMenuItems removes several items in one transaction (all or none) and returns
// their photo ids.
func (s *Store) DeleteMenuItems(ctx context.Context, instanceID string, ids []string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	imgs, err := deleteItems(ctx, tx, instanceID, ids)
	if err != nil {
		return nil, err
	}
	return imgs, tx.Commit()
}

func deleteItems(ctx context.Context, tx *sql.Tx, instanceID string, ids []string) ([]string, error) {
	var imgs []string
	for _, id := range ids {
		if err := itemExists(ctx, tx, instanceID, id); err != nil {
			return nil, err
		}
		list, err := itemImageIDs(ctx, tx, instanceID, id)
		if err != nil {
			return nil, err
		}
		var main string
		if err := tx.QueryRowContext(ctx, `SELECT image_id FROM menu_items WHERE instance_id = ? AND id = ?`, instanceID, id).Scan(&main); err != nil {
			return nil, err
		}
		if main != "" && !slices.Contains(list, main) {
			list = append(list, main)
		}
		for _, im := range list {
			if err := dropImage(ctx, tx, instanceID, im); err != nil {
				return nil, err
			}
		}
		imgs = append(imgs, list...)
		if _, err := tx.ExecContext(ctx, `DELETE FROM menu_items WHERE instance_id = ? AND id = ?`, instanceID, id); err != nil {
			return nil, err
		}
	}
	return imgs, nil
}

// ---- collections ----

// MenuCollection is a group of items shown together ("Promoções", "Mais vendidos"),
// independent of the categories.
type MenuCollection struct {
	ID           string
	Name         string
	Description  string
	Position     int
	Active       bool
	CoverImageID string
	SourceRef    string
	// ItemIDs are the collection's items, in order.
	ItemIDs   []string
	CreatedAt time.Time
	UpdatedAt time.Time
}

const collectionCols = `id, name, description, position, active, cover_image_id, source_ref, created_at, updated_at`

func scanCollection(sc interface{ Scan(...any) error }) (MenuCollection, error) {
	var c MenuCollection
	var ca, ua string
	err := sc.Scan(&c.ID, &c.Name, &c.Description, &c.Position, &c.Active, &c.CoverImageID, &c.SourceRef, &ca, &ua)
	c.CreatedAt, c.UpdatedAt = parseTS(ca), parseTS(ua)
	return c, err
}

func listMemberships(ctx context.Context, q queryer, instanceID, itemID string) (map[string][]string, error) {
	query := `SELECT ci.item_id, ci.collection_id FROM menu_collection_items ci JOIN menu_collections c ON c.id = ci.collection_id
		WHERE ci.instance_id = ?`
	args := []any{instanceID}
	if itemID != "" {
		query += ` AND ci.item_id = ?`
		args = append(args, itemID)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY c.position, c.created_at, c.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var it, col string
		if err := rows.Scan(&it, &col); err != nil {
			return nil, err
		}
		out[it] = append(out[it], col)
	}
	return out, rows.Err()
}

func listCollections(ctx context.Context, q queryer, instanceID string) ([]MenuCollection, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+collectionCols+` FROM menu_collections WHERE instance_id = ?
		ORDER BY position, created_at, id`, instanceID)
	if err != nil {
		return nil, err
	}
	out := []MenuCollection{}
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		c.ItemIDs = []string{}
		out = append(out, c)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	members, err := q.QueryContext(ctx, `SELECT collection_id, item_id FROM menu_collection_items WHERE instance_id = ?
		ORDER BY collection_id, position, item_id`, instanceID)
	if err != nil {
		return nil, err
	}
	defer members.Close()
	idx := map[string]int{}
	for i, c := range out {
		idx[c.ID] = i
	}
	for members.Next() {
		var col, it string
		if err := members.Scan(&col, &it); err != nil {
			return nil, err
		}
		if i, ok := idx[col]; ok {
			out[i].ItemIDs = append(out[i].ItemIDs, it)
		}
	}
	return out, members.Err()
}

// ListMenuCollections lists the collections in display order, with their items.
func (s *Store) ListMenuCollections(ctx context.Context, instanceID string) ([]MenuCollection, error) {
	return listCollections(ctx, s.db, instanceID)
}

func getCollection(ctx context.Context, q queryer, instanceID, id string) (MenuCollection, error) {
	c, err := scanCollection(q.QueryRowContext(ctx, `SELECT `+collectionCols+` FROM menu_collections WHERE instance_id = ? AND id = ?`,
		instanceID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MenuCollection{}, ErrNotFound
	}
	if err != nil {
		return MenuCollection{}, err
	}
	c.ItemIDs, err = collectionItems(ctx, q, instanceID, id)
	return c, err
}

func collectionItems(ctx context.Context, q queryer, instanceID, id string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT item_id FROM menu_collection_items WHERE instance_id = ? AND collection_id = ?
		ORDER BY position, item_id`, instanceID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var it string
		if err := rows.Scan(&it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// GetMenuCollection returns one collection with its items (ErrNotFound when unknown).
func (s *Store) GetMenuCollection(ctx context.Context, instanceID, id string) (MenuCollection, error) {
	return getCollection(ctx, s.db, instanceID, id)
}

// MenuCollectionBySource finds a collection copied from elsewhere ("wa:<id>").
func (s *Store) MenuCollectionBySource(ctx context.Context, instanceID, ref string) (MenuCollection, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM menu_collections WHERE instance_id = ? AND source_ref = ?`, instanceID, ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return MenuCollection{}, ErrNotFound
	}
	if err != nil {
		return MenuCollection{}, err
	}
	return s.GetMenuCollection(ctx, instanceID, id)
}

func checkCover(ctx context.Context, q queryer, instanceID, imageID string) error {
	if imageID == "" {
		return nil
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM menu_item_images WHERE instance_id = ? AND id = ?`, instanceID, imageID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func createCollection(ctx context.Context, tx *sql.Tx, instanceID string, c MenuCollection) (MenuCollection, error) {
	var n, pos int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(position) + 1, 0) FROM menu_collections WHERE instance_id = ?`,
		instanceID).Scan(&n, &pos); err != nil {
		return MenuCollection{}, err
	}
	if n >= MaxMenuCollections {
		return MenuCollection{}, ErrMenuLimit
	}
	if err := checkCover(ctx, tx, instanceID, c.CoverImageID); err != nil {
		return MenuCollection{}, err
	}
	ts := now()
	c.ID, c.Position, c.CreatedAt, c.UpdatedAt, c.ItemIDs = secure.NewID("mcol_"), pos, parseTS(ts), parseTS(ts), []string{}
	res, err := tx.ExecContext(ctx, `INSERT INTO menu_collections (id, instance_id, name, description, position, active, cover_image_id,
		source_ref, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		c.ID, instanceID, c.Name, c.Description, pos, c.Active, c.CoverImageID, c.SourceRef, ts, ts)
	if err != nil {
		return MenuCollection{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return MenuCollection{}, ErrMenuDuplicate
	}
	return c, nil
}

// CreateMenuCollection adds a collection at the end (ErrMenuLimit over
// MaxMenuCollections; a cover that is not a photo of the menu is ErrNotFound).
func (s *Store) CreateMenuCollection(ctx context.Context, instanceID string, c MenuCollection) (MenuCollection, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MenuCollection{}, err
	}
	defer tx.Rollback()
	c, err = createCollection(ctx, tx, instanceID, c)
	if err != nil {
		return MenuCollection{}, err
	}
	return c, tx.Commit()
}

// UpdateMenuCollection replaces a collection's name, description, active flag and cover.
func (s *Store) UpdateMenuCollection(ctx context.Context, instanceID string, c MenuCollection) (MenuCollection, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MenuCollection{}, err
	}
	defer tx.Rollback()
	if err := checkCover(ctx, tx, instanceID, c.CoverImageID); err != nil {
		return MenuCollection{}, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE menu_collections SET name = ?, description = ?, active = ?, cover_image_id = ?, updated_at = ?
		WHERE instance_id = ? AND id = ?`, c.Name, c.Description, c.Active, c.CoverImageID, now(), instanceID, c.ID)
	if err != nil {
		return MenuCollection{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return MenuCollection{}, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return MenuCollection{}, err
	}
	return s.GetMenuCollection(ctx, instanceID, c.ID)
}

// DeleteMenuCollection removes a collection (its items stay in the menu).
func (s *Store) DeleteMenuCollection(ctx context.Context, instanceID, id string) error {
	return s.exec1(ctx, `DELETE FROM menu_collections WHERE instance_id = ? AND id = ?`, instanceID, id)
}

// ReorderMenuCollections sets the positions of the listed collections, in that order.
func (s *Store) ReorderMenuCollections(ctx context.Context, instanceID string, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		res, err := tx.ExecContext(ctx, `UPDATE menu_collections SET position = ? WHERE instance_id = ? AND id = ?`, i, instanceID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
	}
	return tx.Commit()
}

// addToCollection appends items to a collection (those already in it stay where they
// are). Unknown items or collection are ErrNotFound; over MaxCollectionItems,
// ErrMenuLimit.
func addToCollection(ctx context.Context, tx *sql.Tx, instanceID, collectionID string, itemIDs []string) error {
	cur, err := collectionItems(ctx, tx, instanceID, collectionID)
	if err != nil {
		return err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM menu_collections WHERE instance_id = ? AND id = ?`, instanceID, collectionID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	pos := len(cur)
	for _, it := range itemIDs {
		if slices.Contains(cur, it) {
			continue
		}
		if err := itemExists(ctx, tx, instanceID, it); err != nil {
			return err
		}
		if len(cur) >= MaxCollectionItems {
			return ErrMenuLimit
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO menu_collection_items (collection_id, item_id, instance_id, position) VALUES (?, ?, ?, ?)`,
			collectionID, it, instanceID, pos); err != nil {
			return err
		}
		cur = append(cur, it)
		pos++
	}
	_, err = tx.ExecContext(ctx, `UPDATE menu_collections SET updated_at = ? WHERE instance_id = ? AND id = ?`, now(), instanceID, collectionID)
	return err
}

// setItemCollections makes an item a member of exactly the given collections.
func setItemCollections(ctx context.Context, tx *sql.Tx, instanceID, itemID string, cur, want []string) error {
	for _, c := range cur {
		if !slices.Contains(want, c) {
			if _, err := tx.ExecContext(ctx, `DELETE FROM menu_collection_items WHERE instance_id = ? AND collection_id = ? AND item_id = ?`,
				instanceID, c, itemID); err != nil {
				return err
			}
		}
	}
	for _, c := range want {
		if !slices.Contains(cur, c) {
			if err := addToCollection(ctx, tx, instanceID, c, []string{itemID}); err != nil {
				return err
			}
		}
	}
	return nil
}

// AddMenuCollectionItems adds items to a collection (all or none).
func (s *Store) AddMenuCollectionItems(ctx context.Context, instanceID, collectionID string, itemIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := addToCollection(ctx, tx, instanceID, collectionID, itemIDs); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveMenuCollectionItems takes items out of a collection (the items stay in the menu)
// and returns how many were in it.
func (s *Store) RemoveMenuCollectionItems(ctx context.Context, instanceID, collectionID string, itemIDs []string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	cur, err := getCollection(ctx, tx, instanceID, collectionID)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, it := range itemIDs {
		res, err := tx.ExecContext(ctx, `DELETE FROM menu_collection_items WHERE instance_id = ? AND collection_id = ? AND item_id = ?`,
			instanceID, collectionID, it)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		removed += int(n)
	}
	left := []string{}
	for _, it := range cur.ItemIDs {
		if !slices.Contains(itemIDs, it) {
			left = append(left, it)
		}
	}
	if err := writeCollectionOrder(ctx, tx, instanceID, collectionID, left); err != nil {
		return 0, err
	}
	return removed, tx.Commit()
}

func writeCollectionOrder(ctx context.Context, tx *sql.Tx, instanceID, collectionID string, ids []string) error {
	for i, it := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE menu_collection_items SET position = ? WHERE instance_id = ? AND collection_id = ? AND item_id = ?`,
			i, instanceID, collectionID, it); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE menu_collections SET updated_at = ? WHERE instance_id = ? AND id = ?`, now(), instanceID, collectionID)
	return err
}

// ReorderMenuCollectionItems sets the order of a collection's items: ids lists all of
// them (ErrMenuOrder otherwise).
func (s *Store) ReorderMenuCollectionItems(ctx context.Context, instanceID, collectionID string, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cur, err := getCollection(ctx, tx, instanceID, collectionID)
	if err != nil {
		return err
	}
	if len(cur.ItemIDs) != len(ids) {
		return ErrMenuOrder
	}
	for _, id := range ids {
		if !slices.Contains(cur.ItemIDs, id) {
			return ErrMenuOrder
		}
	}
	if err := writeCollectionOrder(ctx, tx, instanceID, collectionID, ids); err != nil {
		return err
	}
	return tx.Commit()
}

// MenuCollectionCount returns the number of collections of an instance.
func (s *Store) MenuCollectionCount(ctx context.Context, instanceID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM menu_collections WHERE instance_id = ?`, instanceID).Scan(&n)
	return n, err
}

// ---- bulk changes and duplicates ----

// UpdateMenuItems applies fn to each listed item and saves its category, prices, sale
// end, availability and hidden flag, in one transaction: any error (an unknown item, an
// unknown category, fn's own) changes nothing. An item moved to another category goes
// to its end.
func (s *Store) UpdateMenuItems(ctx context.Context, instanceID string, ids []string, fn func(*MenuItem) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	for _, id := range ids {
		it, err := getItem(ctx, tx, instanceID, id)
		if err != nil {
			return err
		}
		from := it.CategoryID
		if err := fn(&it); err != nil {
			return err
		}
		pos := it.Position
		if it.CategoryID != from {
			ok, err := categoryExists(ctx, tx, instanceID, it.CategoryID)
			if err != nil {
				return err
			}
			if !ok {
				return ErrNotFound
			}
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position) + 1, 0) FROM menu_items WHERE instance_id = ? AND category_id = ?`,
				instanceID, it.CategoryID).Scan(&pos); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE menu_items SET category_id = ?, position = ?, price_cents = ?, sale_price_cents = ?,
			sale_until = ?, available = ?, hidden = ?, updated_at = ? WHERE instance_id = ? AND id = ?`,
			it.CategoryID, pos, it.PriceCents, it.SalePriceCents, it.SaleUntil, it.Available, it.Hidden, ts, instanceID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DuplicateMenuItem copies an item (fields, options, collections where there is room
// and photos) as a new item at the end of its category. photos maps each photo id of the
// source to the id of its copy (the caller copied the files); photos left out are not
// copied. The copy gets name, no sku and no source_ref.
func (s *Store) DuplicateMenuItem(ctx context.Context, instanceID, id, name string, photos map[string]string) (MenuItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MenuItem{}, err
	}
	defer tx.Rollback()
	src, err := getItem(ctx, tx, instanceID, id)
	if err != nil {
		return MenuItem{}, err
	}
	cp := src
	cp.Name, cp.SKU, cp.SourceRef, cp.CollectionIDs = name, "", "", nil
	for i := range cp.Options {
		cp.Options[i].ID = ""
	}
	created, err := createItem(ctx, tx, instanceID, cp)
	if err != nil {
		return MenuItem{}, err
	}
	for _, c := range src.CollectionIDs {
		err := addToCollection(ctx, tx, instanceID, c, []string{created.ID})
		if errors.Is(err, ErrMenuLimit) { // a full collection: the copy stays out of it
			continue
		}
		if err != nil {
			return MenuItem{}, err
		}
	}
	ids := []string{}
	for _, im := range src.Images {
		nid, ok := photos[im.ID]
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO menu_item_images (id, instance_id, item_id, position, source_url, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, nid, instanceID, created.ID, len(ids), im.SourceURL, now()); err != nil {
			return MenuItem{}, err
		}
		ids = append(ids, nid)
	}
	if err := writeImageOrder(ctx, tx, instanceID, created.ID, ids); err != nil {
		return MenuItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return MenuItem{}, err
	}
	return s.GetMenuItem(ctx, instanceID, created.ID)
}

// ---- order drafts ----

// Order draft statuses.
const (
	OrderDraft     = "draft"
	OrderSent      = "sent"
	OrderConfirmed = "confirmed"
	OrderCancelled = "cancelled"
)

// MaxOrderDrafts is how many drafts an instance keeps (the oldest are removed).
const MaxOrderDrafts = 1000

// ErrOrderStatus is a status change that is not allowed (a cancelled draft is final).
var ErrOrderStatus = errors.New("order draft status change not allowed")

// OrderDraftRow is a stored order draft. Items is the engine's JSON of the lines.
type OrderDraftRow struct {
	ID            string
	Chat          string
	CustomerName  string
	Address       string
	Notes         string
	Items         json.RawMessage
	SubtotalCents int64
	TotalCents    int64
	Currency      string
	Valid         bool
	Status        string
	Source        string
	MessageID     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const draftCols = `id, chat, customer_name, address, notes, items_json, subtotal_cents, total_cents, currency, valid, status, source,
	message_id, created_at, updated_at`

func scanDraft(sc interface{ Scan(...any) error }) (OrderDraftRow, error) {
	var d OrderDraftRow
	var items, ca, ua string
	err := sc.Scan(&d.ID, &d.Chat, &d.CustomerName, &d.Address, &d.Notes, &items, &d.SubtotalCents, &d.TotalCents, &d.Currency,
		&d.Valid, &d.Status, &d.Source, &d.MessageID, &ca, &ua)
	d.Items = json.RawMessage(items)
	d.CreatedAt, d.UpdatedAt = parseTS(ca), parseTS(ua)
	return d, err
}

// CreateOrderDraft stores a draft (status draft, with d.ID when set) and removes the
// oldest drafts over MaxOrderDrafts.
func (s *Store) CreateOrderDraft(ctx context.Context, instanceID string, d OrderDraftRow) (OrderDraftRow, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OrderDraftRow{}, err
	}
	defer tx.Rollback()
	ts := now()
	if d.ID == "" { // the attendant decides the id before publishing its event
		d.ID = secure.NewID("odr_")
	}
	d.Status = OrderDraft
	if d.Currency == "" {
		d.Currency = "BRL"
	}
	if len(d.Items) == 0 {
		d.Items = json.RawMessage("[]")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO order_drafts (id, instance_id, chat, customer_name, address, notes, items_json, subtotal_cents,
		total_cents, currency, valid, status, source, message_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?)`,
		d.ID, instanceID, d.Chat, d.CustomerName, d.Address, d.Notes, string(d.Items), d.SubtotalCents, d.TotalCents, d.Currency, d.Valid,
		d.Status, d.Source, ts, ts); err != nil {
		return OrderDraftRow{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM order_drafts WHERE instance_id = ? AND id NOT IN
		(SELECT id FROM order_drafts WHERE instance_id = ? ORDER BY created_at DESC, id DESC LIMIT ?)`, instanceID, instanceID, MaxOrderDrafts); err != nil {
		return OrderDraftRow{}, err
	}
	if err := tx.Commit(); err != nil {
		return OrderDraftRow{}, err
	}
	return s.GetOrderDraft(ctx, instanceID, d.ID)
}

// ReplaceOrderDraftItems updates the lines and totals of a draft still in status draft
// (the customer changed the order). ErrOrderStatus once it was sent, confirmed or
// cancelled.
func (s *Store) ReplaceOrderDraftItems(ctx context.Context, instanceID string, d OrderDraftRow) (OrderDraftRow, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE order_drafts SET customer_name = ?, address = ?, notes = ?, items_json = ?, subtotal_cents = ?,
		total_cents = ?, currency = ?, valid = ?, updated_at = ? WHERE instance_id = ? AND id = ? AND status = 'draft'`,
		d.CustomerName, d.Address, d.Notes, string(d.Items), d.SubtotalCents, d.TotalCents, d.Currency, d.Valid, now(), instanceID, d.ID)
	if err != nil {
		return OrderDraftRow{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetOrderDraft(ctx, instanceID, d.ID); err != nil {
			return OrderDraftRow{}, err
		}
		return OrderDraftRow{}, ErrOrderStatus
	}
	return s.GetOrderDraft(ctx, instanceID, d.ID)
}

// GetOrderDraft returns one draft (ErrNotFound when unknown).
func (s *Store) GetOrderDraft(ctx context.Context, instanceID, id string) (OrderDraftRow, error) {
	d, err := scanDraft(s.db.QueryRowContext(ctx, `SELECT `+draftCols+` FROM order_drafts WHERE instance_id = ? AND id = ?`, instanceID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return OrderDraftRow{}, ErrNotFound
	}
	return d, err
}

// OrderDraftFilter narrows ListOrderDrafts ("" = any).
type OrderDraftFilter struct {
	Status string
	Chat   string
	Source string
	Limit  int
}

// ListOrderDrafts lists drafts, newest first.
func (s *Store) ListOrderDrafts(ctx context.Context, instanceID string, f OrderDraftFilter) ([]OrderDraftRow, error) {
	q := `SELECT ` + draftCols + ` FROM order_drafts WHERE instance_id = ?`
	args := []any{instanceID}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.Chat != "" {
		q += ` AND chat = ?`
		args = append(args, f.Chat)
	}
	if f.Source != "" {
		q += ` AND source = ?`
		args = append(args, f.Source)
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OrderDraftRow{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// orderMoves are the allowed status changes.
var orderMoves = map[string][]string{
	OrderDraft:     {OrderSent, OrderConfirmed, OrderCancelled},
	OrderSent:      {OrderDraft, OrderConfirmed, OrderCancelled},
	OrderConfirmed: {OrderCancelled},
}

// SetOrderDraftStatus changes a draft's status (ErrOrderStatus when the move is not
// allowed: a cancelled draft is final, a confirmed one can only be cancelled). A
// messageID, when given, records the summary sent.
func (s *Store) SetOrderDraftStatus(ctx context.Context, instanceID, id, status, messageID string) (OrderDraftRow, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OrderDraftRow{}, err
	}
	defer tx.Rollback()
	var cur string
	err = tx.QueryRowContext(ctx, `SELECT status FROM order_drafts WHERE instance_id = ? AND id = ?`, instanceID, id).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		return OrderDraftRow{}, ErrNotFound
	}
	if err != nil {
		return OrderDraftRow{}, err
	}
	if cur != status && !slices.Contains(orderMoves[cur], status) {
		return OrderDraftRow{}, ErrOrderStatus
	}
	q, args := `UPDATE order_drafts SET status = ?, updated_at = ?`, []any{status, now()}
	if messageID != "" {
		q += `, message_id = ?`
		args = append(args, messageID)
	}
	args = append(args, instanceID, id)
	if _, err := tx.ExecContext(ctx, q+` WHERE instance_id = ? AND id = ?`, args...); err != nil {
		return OrderDraftRow{}, err
	}
	if err := tx.Commit(); err != nil {
		return OrderDraftRow{}, err
	}
	return s.GetOrderDraft(ctx, instanceID, id)
}

// OpenOrderDraft returns the newest draft of a chat still in status draft from source,
// created after since (ErrNotFound when none): the attendant updates it when the
// customer changes the order.
func (s *Store) OpenOrderDraft(ctx context.Context, instanceID, chat, source string, since time.Time) (OrderDraftRow, error) {
	d, err := scanDraft(s.db.QueryRowContext(ctx, `SELECT `+draftCols+` FROM order_drafts WHERE instance_id = ? AND chat = ? AND source = ?
		AND status = 'draft' AND created_at >= ? ORDER BY created_at DESC, id DESC LIMIT 1`, instanceID, chat, source, fmtTS(since)))
	if errors.Is(err, sql.ErrNoRows) {
		return OrderDraftRow{}, ErrNotFound
	}
	return d, err
}
