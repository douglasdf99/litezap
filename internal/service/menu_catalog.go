package service

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/menu"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/store"
)

// ---- Cardápio, parity with the WhatsApp catalog: collections, search, duplicates,
// bulk changes and the CSV export. Nothing here reads from or writes to Meta. ----

// MenuCollectionView is a collection ("Promoções") with its item ids in order.
type MenuCollectionView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Position    int    `json:"position"`
	// Active collections are the ones offered to customers (send_collection refuses an
	// inactive one; the attendant only lists active ones).
	Active bool `json:"active"`
	// CoverImageID is a photo of one of the menu's items (CoverPath downloads it).
	CoverImageID string    `json:"cover_image_id,omitempty"`
	CoverPath    string    `json:"cover_path,omitempty"`
	ItemIDs      []string  `json:"item_ids"`
	ItemCount    int       `json:"item_count"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// MenuCollectionDetail is a collection with its items.
type MenuCollectionDetail struct {
	MenuCollectionView
	Items []MenuItemView `json:"items"`
}

func collectionView(instanceID string, c store.MenuCollection, items []store.MenuItem) MenuCollectionView {
	v := MenuCollectionView{ID: c.ID, Name: c.Name, Description: c.Description, Position: c.Position, Active: c.Active,
		CoverImageID: c.CoverImageID, ItemIDs: append([]string{}, c.ItemIDs...), ItemCount: len(c.ItemIDs), UpdatedAt: c.UpdatedAt}
	if c.CoverImageID != "" {
		for _, it := range items {
			for _, im := range it.Images {
				if im.ID == c.CoverImageID {
					v.CoverPath = imagePath(instanceID, it.ID, im.ID)
				}
			}
		}
	}
	return v
}

// MenuCollectionInput creates a collection or patches one (absent fields stay). The
// cover is cover_image_id (a photo id) or cover_item_id (that item's main photo); ""
// removes it. item_ids, on create, are its first items.
type MenuCollectionInput struct {
	Name         *string   `json:"name,omitempty"`
	Description  *string   `json:"description,omitempty"`
	Active       *bool     `json:"active,omitempty"`
	CoverImageID *string   `json:"cover_image_id,omitempty"`
	CoverItemID  *string   `json:"cover_item_id,omitempty"`
	ItemIDs      *[]string `json:"item_ids,omitempty"`
}

// MenuItemIDsInput lists items (to add to or remove from a collection).
type MenuItemIDsInput struct {
	ItemIDs []string `json:"item_ids"`
}

func checkItemIDs(ids []string, limit int) ([]string, error) {
	out := []string{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if !validMenuID("mitm", id) {
			return nil, apierr.Validation("item_ids lists menu item ids (mitm_…, from GET /menu or search)")
		}
		if !containsStr(out, id) {
			out = append(out, id)
		}
	}
	if len(out) == 0 || len(out) > limit {
		return nil, apierr.Validation(fmt.Sprintf("item_ids lists 1 to %d items", limit))
	}
	return out, nil
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ListCollections lists the collections in display order.
func (m *Menu) ListCollections(ctx context.Context, instanceID string) ([]MenuCollectionView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return nil, err
	}
	colls, err := m.Store.ListMenuCollections(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	items, err := m.Store.ListMenuItems(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	out := []MenuCollectionView{}
	for _, c := range colls {
		out = append(out, collectionView(instanceID, c, items))
	}
	return out, nil
}

// Collection returns one collection with its items, in order.
func (m *Menu) Collection(ctx context.Context, instanceID, id string) (MenuCollectionDetail, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuCollectionDetail{}, err
	}
	c, err := m.Store.GetMenuCollection(ctx, instanceID, id)
	if err != nil {
		return MenuCollectionDetail{}, menuErr(err, "menu collection")
	}
	items, err := m.Store.ListMenuItems(ctx, instanceID)
	if err != nil {
		return MenuCollectionDetail{}, err
	}
	d := MenuCollectionDetail{MenuCollectionView: collectionView(instanceID, c, items), Items: []MenuItemView{}}
	for _, it := range menu.CollectionItems(c, items) {
		d.Items = append(d.Items, itemView(instanceID, it))
	}
	return d, nil
}

func collectionName(s string) (string, error) {
	n := menu.CleanLine(s)
	if n == "" || utf8.RuneCountInString(n) > menu.MaxCollectionLen {
		return "", apierr.Validation(fmt.Sprintf("name is required (at most %d characters)", menu.MaxCollectionLen))
	}
	return n, nil
}

// cover resolves the cover of a collection input (cur: the current cover).
func (m *Menu) cover(ctx context.Context, instanceID string, in MenuCollectionInput, cur string) (string, error) {
	switch {
	case in.CoverImageID != nil && in.CoverItemID != nil:
		return "", apierr.Validation("send cover_image_id or cover_item_id, not both")
	case in.CoverImageID != nil:
		id := strings.TrimSpace(*in.CoverImageID)
		if id != "" && !validMenuID("mimg", id) {
			return "", apierr.Validation("cover_image_id is the id of a photo of a menu item (mimg_…)")
		}
		return id, nil
	case in.CoverItemID != nil:
		id := strings.TrimSpace(*in.CoverItemID)
		if id == "" {
			return "", nil
		}
		it, err := m.Store.GetMenuItem(ctx, instanceID, id)
		if err != nil {
			return "", menuErr(err, "menu item")
		}
		if it.ImageID == "" {
			return "", apierr.Validation("cover_item_id: this item has no photo")
		}
		return it.ImageID, nil
	}
	return cur, nil
}

// CreateCollection adds a collection (at most store.MaxMenuCollections).
func (m *Menu) CreateCollection(ctx context.Context, instanceID string, in MenuCollectionInput) (MenuCollectionDetail, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuCollectionDetail{}, err
	}
	if in.Name == nil {
		return MenuCollectionDetail{}, apierr.Validation("name is required")
	}
	c := store.MenuCollection{Active: true}
	if err := m.applyCollection(ctx, instanceID, in, &c); err != nil {
		return MenuCollectionDetail{}, err
	}
	var ids []string
	if in.ItemIDs != nil && len(*in.ItemIDs) > 0 {
		var err error
		if ids, err = checkItemIDs(*in.ItemIDs, store.MaxCollectionItems); err != nil {
			return MenuCollectionDetail{}, err
		}
	}
	created, err := m.Store.CreateMenuCollection(ctx, instanceID, c)
	if err != nil {
		return MenuCollectionDetail{}, menuErr(err, "cover photo")
	}
	if len(ids) > 0 {
		if err := m.Store.AddMenuCollectionItems(ctx, instanceID, created.ID, ids); err != nil {
			_ = m.Store.DeleteMenuCollection(ctx, instanceID, created.ID) // all or nothing
			return MenuCollectionDetail{}, menuErr(err, "menu item")
		}
	}
	return m.Collection(ctx, instanceID, created.ID)
}

func (m *Menu) applyCollection(ctx context.Context, instanceID string, in MenuCollectionInput, c *store.MenuCollection) error {
	if in.Name != nil {
		n, err := collectionName(*in.Name)
		if err != nil {
			return err
		}
		c.Name = n
	}
	if in.Description != nil {
		d := menu.CleanText(*in.Description)
		if utf8.RuneCountInString(d) > menu.MaxCollectionDesc {
			return apierr.Validation(fmt.Sprintf("description is limited to %d characters", menu.MaxCollectionDesc))
		}
		c.Description = d
	}
	if in.Active != nil {
		c.Active = *in.Active
	}
	cov, err := m.cover(ctx, instanceID, in, c.CoverImageID)
	if err != nil {
		return err
	}
	c.CoverImageID = cov
	return nil
}

// UpdateCollection changes a collection's name, description, active flag or cover.
func (m *Menu) UpdateCollection(ctx context.Context, instanceID, id string, in MenuCollectionInput) (MenuCollectionDetail, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuCollectionDetail{}, err
	}
	if in.ItemIDs != nil {
		return MenuCollectionDetail{}, apierr.Validation("item_ids only goes with a new collection: add or remove items with .../items")
	}
	c, err := m.Store.GetMenuCollection(ctx, instanceID, id)
	if err != nil {
		return MenuCollectionDetail{}, menuErr(err, "menu collection")
	}
	if err := m.applyCollection(ctx, instanceID, in, &c); err != nil {
		return MenuCollectionDetail{}, err
	}
	if _, err := m.Store.UpdateMenuCollection(ctx, instanceID, c); err != nil {
		return MenuCollectionDetail{}, menuErr(err, "menu collection or cover photo")
	}
	return m.Collection(ctx, instanceID, id)
}

// DeleteCollection removes a collection (its items stay in the menu).
func (m *Menu) DeleteCollection(ctx context.Context, instanceID, id string) error {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return err
	}
	return menuErr(m.Store.DeleteMenuCollection(ctx, instanceID, id), "menu collection")
}

// ReorderCollections sets the order of the collections.
func (m *Menu) ReorderCollections(ctx context.Context, instanceID string, in ReorderInput) error {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return err
	}
	if err := checkIDs(in.IDs, store.MaxMenuCollections); err != nil {
		return err
	}
	return menuErr(m.Store.ReorderMenuCollections(ctx, instanceID, in.IDs), "menu collection")
}

// AddCollectionItems adds items at the end of a collection (all or none; those already
// in it stay where they are).
func (m *Menu) AddCollectionItems(ctx context.Context, instanceID, id string, in MenuItemIDsInput) (MenuCollectionDetail, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuCollectionDetail{}, err
	}
	ids, err := checkItemIDs(in.ItemIDs, store.MaxCollectionItems)
	if err != nil {
		return MenuCollectionDetail{}, err
	}
	if err := m.Store.AddMenuCollectionItems(ctx, instanceID, id, ids); err != nil {
		return MenuCollectionDetail{}, menuErr(err, "menu collection or item")
	}
	return m.Collection(ctx, instanceID, id)
}

// RemoveCollectionItems takes items out of a collection (they stay in the menu).
func (m *Menu) RemoveCollectionItems(ctx context.Context, instanceID, id string, in MenuItemIDsInput) (MenuCollectionDetail, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuCollectionDetail{}, err
	}
	ids, err := checkItemIDs(in.ItemIDs, store.MaxMenuItems)
	if err != nil {
		return MenuCollectionDetail{}, err
	}
	if _, err := m.Store.RemoveMenuCollectionItems(ctx, instanceID, id, ids); err != nil {
		return MenuCollectionDetail{}, menuErr(err, "menu collection")
	}
	return m.Collection(ctx, instanceID, id)
}

// ReorderCollectionItems sets the order of a collection's items (ids lists all of them).
func (m *Menu) ReorderCollectionItems(ctx context.Context, instanceID, id string, in ReorderInput) (MenuCollectionDetail, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuCollectionDetail{}, err
	}
	if err := checkIDs(in.IDs, store.MaxCollectionItems); err != nil {
		return MenuCollectionDetail{}, err
	}
	if err := m.Store.ReorderMenuCollectionItems(ctx, instanceID, id, in.IDs); err != nil {
		return MenuCollectionDetail{}, menuErr(err, "menu collection")
	}
	return m.Collection(ctx, instanceID, id)
}

// ---- sending a collection ----

// MaxCollectionPhotos bounds the photos one send_collection sends.
const MaxCollectionPhotos = 10

// SendCollectionInput sends a collection: its text (split when long) and, optionally,
// the photos of its first items (one message each, with name and price).
type SendCollectionInput struct {
	To                 string `json:"to"`
	Title              string `json:"title,omitempty"`
	IncludeUnavailable bool   `json:"include_unavailable,omitempty"`
	// Photos sends the photos of the first N listed items that have one (0-10).
	Photos int `json:"photos,omitempty"`
	// MaxMessages refuses a text that needs more messages (default and max 10).
	MaxMessages     int    `json:"max_messages,omitempty"`
	ReplyTo         string `json:"reply_to,omitempty"`
	ClientMessageID string `json:"client_message_id,omitempty"`
	ExpiresAfter    string `json:"expires_after,omitempty"`
}

// SendCollection queues a collection's text and photos through the send queue. With a
// client_message_id, message n uses "<id>:n", so a retry never sends twice.
func (m *Menu) SendCollection(ctx context.Context, instanceID, id string, in SendCollectionInput, wait time.Duration) (SendMenuResult, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return SendMenuResult{}, err
	}
	if m.Sender == nil {
		return SendMenuResult{}, apierr.New(http.StatusNotImplemented, "not_implemented", "sending is not available", false)
	}
	if in.Photos < 0 || in.Photos > MaxCollectionPhotos {
		return SendMenuResult{}, apierr.Validation(fmt.Sprintf("photos must be between 0 and %d", MaxCollectionPhotos))
	}
	maxMsgs, clientID, err := sendLimits(in.MaxMessages, in.ClientMessageID)
	if err != nil {
		return SendMenuResult{}, err
	}
	c, err := m.Store.GetMenuCollection(ctx, instanceID, id)
	if err != nil {
		return SendMenuResult{}, menuErr(err, "menu collection")
	}
	if !c.Active {
		return SendMenuResult{}, apierr.Validation("this collection is inactive: activate it before sending it")
	}
	chunks, err := m.chunks(ctx, instanceID, MenuTextInput{CollectionID: id, IncludeUnavailable: in.IncludeUnavailable, Title: in.Title})
	if err != nil {
		return SendMenuResult{}, err
	}
	if len(chunks) == 0 {
		return SendMenuResult{}, apierr.Validation("the collection has no available items to send")
	}
	if len(chunks) > maxMsgs {
		return SendMenuResult{}, apierr.Validation(fmt.Sprintf("the collection needs %d messages and max_messages is %d", len(chunks), maxMsgs))
	}
	msgs := make([]SendInput, 0, len(chunks)+in.Photos)
	for _, t := range chunks {
		msgs = append(msgs, SendInput{To: in.To, Type: "text", Text: t, ExpiresAfter: in.ExpiresAfter})
	}
	if in.Photos > 0 {
		items, err := m.Store.ListMenuItems(ctx, instanceID)
		if err != nil {
			return SendMenuResult{}, err
		}
		n := 0
		for _, it := range menu.CollectionItems(c, items) {
			if n >= in.Photos {
				break
			}
			if it.Hidden || it.ImageID == "" || !it.Available {
				continue // hidden, without photo or sold out (type menu_item refuses it)
			}
			msgs = append(msgs, SendInput{To: in.To, Type: "menu_item", MenuItemID: it.ID, MenuCaption: "short", ExpiresAfter: in.ExpiresAfter})
			n++
		}
	}
	msgs[0].ReplyTo = in.ReplyTo
	return m.sendSeries(ctx, instanceID, msgs, clientID, wait)
}

// sendLimits checks max_messages and the idempotency key (generated when missing).
func sendLimits(maxMsgs int, clientID string) (int, string, error) {
	if maxMsgs == 0 {
		maxMsgs = defaultMenuMsgs
	}
	if maxMsgs < 1 || maxMsgs > menu.MaxChunks {
		return 0, "", apierr.Validation(fmt.Sprintf("max_messages must be between 1 and %d", menu.MaxChunks))
	}
	if len(clientID) > 120 {
		return 0, "", apierr.Validation("client_message_id is limited to 120 characters here")
	}
	if clientID == "" {
		clientID = secure.NewID("menu_")
	}
	return maxMsgs, clientID, nil
}

// sendSeries queues messages in order (message n with client id "<id>:n"); a failure
// after the first one reports how many were queued.
func (m *Menu) sendSeries(ctx context.Context, instanceID string, msgs []SendInput, clientID string, wait time.Duration) (SendMenuResult, error) {
	res := SendMenuResult{ClientMessageID: clientID, Messages: []MessageView{}}
	for i, si := range msgs {
		si.ClientMessageID = fmt.Sprintf("%s:%d", clientID, i+1)
		v, err := m.Sender.Send(ctx, instanceID, si, 0)
		if err != nil {
			if i == 0 {
				return SendMenuResult{}, err
			}
			return res, apierr.New(http.StatusConflict, "partially_queued",
				fmt.Sprintf("%d of %d messages were queued; the rest failed: %s", i, len(msgs), errMessage(err)), true)
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

// SendItemPhotosInput sends an item's photos as an album: the main photo with the
// item's caption, then the others (one message each, in order).
type SendItemPhotosInput struct {
	To              string `json:"to"`
	Caption         string `json:"caption,omitempty"`
	MaxPhotos       int    `json:"max_photos,omitempty"` // default and max 10
	ReplyTo         string `json:"reply_to,omitempty"`
	ClientMessageID string `json:"client_message_id,omitempty"`
	ExpiresAfter    string `json:"expires_after,omitempty"`
}

// SendItemAlbum queues an item's photos (WhatsApp shows consecutive photos as an
// album). An item with one photo or none is sent like type menu_item.
func (m *Menu) SendItemAlbum(ctx context.Context, instanceID, itemID string, in SendItemPhotosInput, wait time.Duration) (SendMenuResult, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return SendMenuResult{}, err
	}
	if m.Sender == nil {
		return SendMenuResult{}, apierr.New(http.StatusNotImplemented, "not_implemented", "sending is not available", false)
	}
	if in.MaxPhotos == 0 {
		in.MaxPhotos = store.MaxMenuImages
	}
	if in.MaxPhotos < 1 || in.MaxPhotos > store.MaxMenuImages {
		return SendMenuResult{}, apierr.Validation(fmt.Sprintf("max_photos must be between 1 and %d", store.MaxMenuImages))
	}
	_, clientID, err := sendLimits(0, in.ClientMessageID)
	if err != nil {
		return SendMenuResult{}, err
	}
	it, err := m.Store.GetMenuItem(ctx, instanceID, itemID)
	if err != nil {
		return SendMenuResult{}, menuErr(err, "menu item")
	}
	msgs := []SendInput{{To: in.To, Type: "menu_item", MenuItemID: it.ID, Caption: in.Caption, ReplyTo: in.ReplyTo, ExpiresAfter: in.ExpiresAfter}}
	for i, im := range it.Images {
		if i == 0 {
			continue
		}
		if len(msgs) >= in.MaxPhotos {
			break
		}
		msgs = append(msgs, SendInput{To: in.To, Type: "menu_item", MenuItemID: it.ID, MenuImageID: im.ID, MenuCaption: "none",
			ExpiresAfter: in.ExpiresAfter})
	}
	return m.sendSeries(ctx, instanceID, msgs, clientID, wait)
}

// ---- search ----

// MenuSearchInput filters the items: q (name, description, sku or tags, ignoring
// accents and case), tag, collection, category ("" = no category), availability,
// hidden and sale. Every filter is optional.
type MenuSearchInput struct {
	Query        string  `json:"q,omitempty"`
	Tag          string  `json:"tag,omitempty"`
	CollectionID string  `json:"collection_id,omitempty"`
	CategoryID   *string `json:"category_id,omitempty"`
	Available    *bool   `json:"available,omitempty"`
	Hidden       *bool   `json:"hidden,omitempty"`
	OnSale       *bool   `json:"on_sale,omitempty"`
	Limit        int     `json:"limit,omitempty"` // default 50, max 500
	Offset       int     `json:"offset,omitempty"`
}

// MenuSearchView is a page of matching items, in menu order.
type MenuSearchView struct {
	Items  []MenuItemView `json:"items"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

// Search lists the items matching the filters, in menu order.
func (m *Menu) Search(ctx context.Context, instanceID string, in MenuSearchInput) (MenuSearchView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuSearchView{}, err
	}
	if in.Limit == 0 {
		in.Limit = 50
	}
	if in.Limit < 1 || in.Limit > store.MaxMenuItems || in.Offset < 0 {
		return MenuSearchView{}, apierr.Validation(fmt.Sprintf("limit is 1 to %d and offset at least 0", store.MaxMenuItems))
	}
	if utf8.RuneCountInString(in.Query) > 120 {
		return MenuSearchView{}, apierr.Validation("q is limited to 120 characters")
	}
	items, err := m.Store.ListMenuItems(ctx, instanceID)
	if err != nil {
		return MenuSearchView{}, err
	}
	q, tag, today := menu.FoldName(in.Query), menu.FoldName(in.Tag), menu.Today()
	v := MenuSearchView{Items: []MenuItemView{}, Limit: in.Limit, Offset: in.Offset}
	for _, it := range items {
		if !matches(it, q, tag, in, today) {
			continue
		}
		if v.Total >= in.Offset && len(v.Items) < in.Limit {
			v.Items = append(v.Items, itemView(instanceID, it))
		}
		v.Total++
	}
	return v, nil
}

func matches(it store.MenuItem, q, tag string, in MenuSearchInput, today string) bool {
	switch {
	case in.CollectionID != "" && !containsStr(it.CollectionIDs, in.CollectionID),
		in.CategoryID != nil && it.CategoryID != *in.CategoryID,
		in.Available != nil && it.Available != *in.Available,
		in.Hidden != nil && it.Hidden != *in.Hidden,
		in.OnSale != nil && menu.OnSale(it, today) != *in.OnSale:
		return false
	}
	if tag != "" {
		found := false
		for _, t := range it.Tags {
			found = found || menu.FoldName(t) == tag
		}
		if !found {
			return false
		}
	}
	if q == "" {
		return true
	}
	hay := menu.FoldName(it.Name + "\n" + it.Description + "\n" + it.SKU + "\n" + strings.Join(it.Tags, "\n"))
	for _, w := range strings.Fields(q) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// ---- duplicate ----

// MenuDuplicateInput names the copy (default "<name> (cópia)") and optionally puts it
// in another category; photos are copied unless copy_photos=false.
type MenuDuplicateInput struct {
	Name       string  `json:"name,omitempty"`
	CategoryID *string `json:"category_id,omitempty"`
	CopyPhotos *bool   `json:"copy_photos,omitempty"`
}

// DuplicateItem copies an item (fields, options, collections and photos) as a new item,
// to make a variation quickly. The copy has no sku.
func (m *Menu) DuplicateItem(ctx context.Context, instanceID, itemID string, in MenuDuplicateInput) (MenuItemView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuItemView{}, err
	}
	src, err := m.Store.GetMenuItem(ctx, instanceID, itemID)
	if err != nil {
		return MenuItemView{}, menuErr(err, "menu item")
	}
	name := menu.CleanLine(in.Name)
	if name == "" {
		name = clipRunes(src.Name, menu.MaxNameLen-len(" (cópia)")) + " (cópia)"
	}
	if utf8.RuneCountInString(name) > menu.MaxNameLen {
		return MenuItemView{}, apierr.Validation(fmt.Sprintf("name is limited to %d characters", menu.MaxNameLen))
	}
	photos := map[string]string{}
	if in.CopyPhotos == nil || *in.CopyPhotos {
		for _, im := range src.Images {
			b, err := readMenuImage(m.DataDir, instanceID, im.ID)
			if err != nil {
				continue // a missing file: the copy goes without that photo
			}
			nid, err := m.writeImageFile(instanceID, b)
			if err != nil {
				m.dropFiles(instanceID, photos)
				return MenuItemView{}, err
			}
			photos[im.ID] = nid
		}
	}
	cp, err := m.Store.DuplicateMenuItem(ctx, instanceID, itemID, name, photos)
	if err != nil {
		m.dropFiles(instanceID, photos)
		return MenuItemView{}, menuErr(err, "menu item")
	}
	if in.CategoryID != nil && *in.CategoryID != cp.CategoryID {
		cat := strings.TrimSpace(*in.CategoryID)
		if err := m.Store.UpdateMenuItems(ctx, instanceID, []string{cp.ID}, func(it *store.MenuItem) error {
			it.CategoryID = cat
			return nil
		}); err != nil {
			v, _ := m.Item(ctx, instanceID, cp.ID)
			return v, apierr.New(http.StatusUnprocessableEntity, "menu_category_failed",
				"item duplicated ("+cp.ID+") but it could not be moved: "+errMessage(menuErr(err, "menu category")), false)
		}
	}
	return m.Item(ctx, instanceID, cp.ID)
}

func (m *Menu) dropFiles(instanceID string, photos map[string]string) {
	for _, id := range photos {
		m.removeImage(instanceID, id)
	}
}

// ---- bulk changes ----

// Bulk actions.
const (
	BulkSetAvailable = "set_available"
	BulkSetHidden    = "set_hidden"
	BulkMoveCategory = "move_category"
	BulkAddToColl    = "add_to_collection"
	BulkRemoveColl   = "remove_from_collection"
	BulkAdjustPrice  = "adjust_price"
	BulkClearSale    = "clear_sale"
	BulkDelete       = "delete"
)

// MenuBulkInput changes many items at once: set_available (available), set_hidden
// (hidden), move_category (category_id; "" = no category), add_to_collection and
// remove_from_collection (collection_id), adjust_price (percent, e.g. 10 or -15, or
// amount_cents, e.g. 500 or -200; sale prices move the same way), clear_sale and delete
// (destructive). dry_run previews the changes without saving.
type MenuBulkInput struct {
	ItemIDs      []string `json:"item_ids"`
	Action       string   `json:"action"`
	Available    *bool    `json:"available,omitempty"`
	Hidden       *bool    `json:"hidden,omitempty"`
	CategoryID   *string  `json:"category_id,omitempty"`
	CollectionID string   `json:"collection_id,omitempty"`
	Percent      *float64 `json:"percent,omitempty"`
	AmountCents  *int64   `json:"amount_cents,omitempty"`
	DryRun       bool     `json:"dry_run,omitempty"`
}

// MenuBulkChange is what a bulk action does (or would do) to one item.
type MenuBulkChange struct {
	ItemID string `json:"item_id"`
	Name   string `json:"name"`
	Before string `json:"before"`
	After  string `json:"after"`
	// For adjust_price: the prices before and after, in cents.
	PriceCents        int64  `json:"price_cents,omitempty"`
	NewPriceCents     int64  `json:"new_price_cents,omitempty"`
	SalePriceCents    int64  `json:"sale_price_cents,omitempty"`
	NewSalePriceCents int64  `json:"new_sale_price_cents,omitempty"`
	Unchanged         bool   `json:"unchanged,omitempty"`
	Error             string `json:"error,omitempty"`
}

// MenuBulkView is a bulk preview (dry_run) or its outcome.
type MenuBulkView struct {
	DryRun   bool             `json:"dry_run"`
	Action   string           `json:"action"`
	Changes  []MenuBulkChange `json:"changes"`
	Affected int              `json:"affected"`
	Errors   []string         `json:"errors"`
}

var menuBulkActions = []string{BulkSetAvailable, BulkSetHidden, BulkMoveCategory, BulkAddToColl, BulkRemoveColl, BulkAdjustPrice, BulkClearSale, BulkDelete}

func yesNo(v bool, yes, no string) string {
	if v {
		return yes
	}
	return no
}

// adjusted applies a percent or an amount to a price, rounded to the cent.
func adjusted(cents int64, percent *float64, amount *int64) int64 {
	if cents == 0 && percent != nil {
		return 0
	}
	if percent != nil {
		return int64(math.Round(float64(cents) * (1 + *percent/100)))
	}
	return cents + *amount
}

// Bulk previews or applies one action to many items, all or nothing: an item that
// cannot take the change (a price going negative, a sale price no longer lower than the
// price) refuses the whole save, with the reason per item.
func (m *Menu) Bulk(ctx context.Context, instanceID string, in MenuBulkInput) (MenuBulkView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuBulkView{}, err
	}
	ids, err := checkItemIDs(in.ItemIDs, store.MaxMenuItems)
	if err != nil {
		return MenuBulkView{}, err
	}
	if !containsStr(menuBulkActions, in.Action) {
		return MenuBulkView{}, apierr.Validation("action is one of " + strings.Join(menuBulkActions, ", "))
	}
	v := MenuBulkView{DryRun: in.DryRun, Action: in.Action, Changes: []MenuBulkChange{}, Errors: []string{}}
	names := map[string]string{}
	switch in.Action {
	case BulkSetAvailable:
		if in.Available == nil {
			return v, apierr.Validation("set_available needs available (true or false)")
		}
	case BulkSetHidden:
		if in.Hidden == nil {
			return v, apierr.Validation("set_hidden needs hidden (true or false)")
		}
	case BulkMoveCategory:
		if in.CategoryID == nil {
			return v, apierr.Validation(`move_category needs category_id ("" for no category)`)
		}
		cats, err := m.Store.ListMenuCategories(ctx, instanceID)
		if err != nil {
			return v, err
		}
		names[""] = "Outros"
		for _, c := range cats {
			names[c.ID] = c.Name
		}
		if _, ok := names[*in.CategoryID]; !ok {
			return v, notFound("menu category")
		}
	case BulkAddToColl, BulkRemoveColl:
		c, err := m.Store.GetMenuCollection(ctx, instanceID, in.CollectionID)
		if err != nil {
			return v, menuErr(err, "menu collection")
		}
		names[c.ID] = c.Name
		if in.Action == BulkAddToColl {
			add := 0
			for _, id := range ids {
				if !containsStr(c.ItemIDs, id) {
					add++
				}
			}
			if len(c.ItemIDs)+add > store.MaxCollectionItems {
				return v, apierr.New(http.StatusConflict, "menu_limit", fmt.Sprintf("the collection has %d items and this adds %d: the limit is %d",
					len(c.ItemIDs), add, store.MaxCollectionItems), false)
			}
		}
	case BulkAdjustPrice:
		switch {
		case (in.Percent == nil) == (in.AmountCents == nil):
			return v, apierr.Validation("adjust_price needs percent (e.g. 10 or -15) or amount_cents (e.g. 500 or -200), not both")
		case in.Percent != nil && (math.IsNaN(*in.Percent) || *in.Percent < -90 || *in.Percent > 500 || *in.Percent == 0):
			return v, apierr.Validation("percent must be between -90 and 500 (not 0)")
		case in.AmountCents != nil && (*in.AmountCents == 0 || *in.AmountCents > menu.MaxPriceCents || *in.AmountCents < -menu.MaxPriceCents):
			return v, apierr.Validation("amount_cents must not be 0 and is limited to R$ 1.000.000,00")
		}
	}
	items := map[string]store.MenuItem{}
	for _, id := range ids {
		it, err := m.Store.GetMenuItem(ctx, instanceID, id)
		if err != nil {
			return v, menuErr(err, "menu item "+id)
		}
		items[id] = it
	}
	planned := map[string]store.MenuItem{}
	for _, id := range ids {
		it := items[id]
		ch := MenuBulkChange{ItemID: id, Name: it.Name}
		next := it
		switch in.Action {
		case BulkSetAvailable:
			ch.Before, ch.After = yesNo(it.Available, "disponível", "esgotado"), yesNo(*in.Available, "disponível", "esgotado")
			next.Available = *in.Available
		case BulkSetHidden:
			ch.Before, ch.After = yesNo(it.Hidden, "oculto", "visível"), yesNo(*in.Hidden, "oculto", "visível")
			next.Hidden = *in.Hidden
		case BulkMoveCategory:
			ch.Before, ch.After = names[it.CategoryID], names[*in.CategoryID]
			if ch.Before == "" {
				ch.Before = "Outros"
			}
			next.CategoryID = *in.CategoryID
		case BulkAddToColl, BulkRemoveColl:
			member := containsStr(it.CollectionIDs, in.CollectionID)
			ch.Before = yesNo(member, "na coleção", "fora da coleção")
			ch.After = yesNo(in.Action == BulkAddToColl, "na coleção", "fora da coleção")
			ch.Unchanged = member == (in.Action == BulkAddToColl)
		case BulkAdjustPrice:
			ch.PriceCents, ch.SalePriceCents = it.PriceCents, it.SalePriceCents
			next.PriceCents = adjusted(it.PriceCents, in.Percent, in.AmountCents)
			if it.SalePriceCents > 0 {
				next.SalePriceCents = adjusted(it.SalePriceCents, in.Percent, in.AmountCents)
			}
			ch.NewPriceCents, ch.NewSalePriceCents = next.PriceCents, next.SalePriceCents
			ch.Before, ch.After = menu.FormatPrice(it.PriceCents, it.Currency), menu.FormatPrice(next.PriceCents, it.Currency)
			if it.SalePriceCents > 0 {
				ch.Before += " (promo " + menu.FormatPrice(it.SalePriceCents, it.Currency) + ")"
				ch.After += " (promo " + menu.FormatPrice(next.SalePriceCents, it.Currency) + ")"
			}
			switch {
			case next.PriceCents < 0 || next.PriceCents > menu.MaxPriceCents:
				ch.Error = "o preço ficaria fora de R$ 0,00 a R$ 1.000.000,00"
			case it.SalePriceCents > 0 && (next.SalePriceCents <= 0 || next.SalePriceCents >= next.PriceCents):
				ch.Error = "o preço promocional deixaria de ser menor que o preço (use clear_sale antes)"
			}
			ch.Unchanged = next.PriceCents == it.PriceCents && next.SalePriceCents == it.SalePriceCents
		case BulkClearSale:
			ch.Before = yesNo(it.SalePriceCents > 0, "promo "+menu.FormatPrice(it.SalePriceCents, it.Currency), "sem promoção")
			ch.After = "sem promoção"
			next.SalePriceCents, next.SaleUntil = 0, ""
			ch.Unchanged = it.SalePriceCents == 0
		case BulkDelete:
			ch.Before, ch.After = "no cardápio", "apagado (com as fotos)"
		}
		if ch.Error != "" {
			v.Errors = append(v.Errors, fmt.Sprintf("%s: %s", it.Name, ch.Error))
		} else if !ch.Unchanged {
			v.Affected++
		}
		planned[id] = next
		v.Changes = append(v.Changes, ch)
	}
	if in.DryRun {
		return v, nil
	}
	if len(v.Errors) > 0 {
		return v, apierr.Validation("nothing saved: " + strings.Join(v.Errors, "; "))
	}
	switch in.Action {
	case BulkAddToColl:
		err = m.Store.AddMenuCollectionItems(ctx, instanceID, in.CollectionID, ids)
	case BulkRemoveColl:
		_, err = m.Store.RemoveMenuCollectionItems(ctx, instanceID, in.CollectionID, ids)
	case BulkDelete:
		var imgs []string
		if imgs, err = m.Store.DeleteMenuItems(ctx, instanceID, ids); err == nil {
			for _, img := range imgs {
				m.removeImage(instanceID, img)
			}
		}
	default:
		err = m.Store.UpdateMenuItems(ctx, instanceID, ids, func(it *store.MenuItem) error {
			p := planned[it.ID]
			it.Available, it.Hidden, it.CategoryID, it.PriceCents, it.SalePriceCents, it.SaleUntil =
				p.Available, p.Hidden, p.CategoryID, p.PriceCents, p.SalePriceCents, p.SaleUntil
			return nil
		})
	}
	if err != nil {
		return v, menuErr(err, "menu item, category or collection")
	}
	return v, nil
}

// ---- CSV export ----

// ExportCSV renders the menu as a spreadsheet that ImportCSV reads back (photos are
// listed by the address they were downloaded from; uploaded photos have none).
func (m *Menu) ExportCSV(ctx context.Context, instanceID string) ([]byte, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return nil, err
	}
	cats, err := m.Store.ListMenuCategories(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	items, err := m.Store.ListMenuItems(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	colls, err := m.Store.ListMenuCollections(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	return menu.ExportCSV(cats, items, colls, func(im store.MenuImage) string { return im.SourceURL }), nil
}
