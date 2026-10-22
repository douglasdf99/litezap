package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/menu"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/store"
)

// Types menu_item and menu of POST /messages (the Cardápio, LiteZap's own catalog): they
// become an ordinary image with caption, or a text, built from the stored menu, and go
// through the send queue like any message. Nothing is read from or written to Meta.

const maxMenuIntro = 1024

// composeMenuItem sends an item: its photo (the main one, or menu_image_id) with the
// caption (name, price, description, options; menu_caption "short" or "none" for
// less), or the caption as a text when it has no photo. caption, when set, comes first
// (an intro line). Hidden and sold-out items are refused.
func (s *Messaging) composeMenuItem(ctx context.Context, instanceID, id string, in SendInput, p *queue.Payload) error {
	if !validMenuID("mitm", in.MenuItemID) {
		return apierr.Validation("menu_item_id is required: the id of a menu item (GET /menu)")
	}
	if in.Text != "" || in.Media != nil {
		return apierr.Validation("type menu_item takes menu_item_id and an optional caption (not text or media)")
	}
	if len(in.Caption) > maxMenuIntro {
		return apierr.Validation("caption is limited to 1024 bytes for menu_item")
	}
	switch in.MenuCaption {
	case "", "full", "short", "none":
	default:
		return apierr.Validation(`menu_caption is "full" (default), "short" or "none"`)
	}
	if in.MenuImageID != "" && !validMenuID("mimg", in.MenuImageID) {
		return apierr.Validation("menu_image_id is the id of a photo of the item (mimg_…)")
	}
	it, err := s.Store.GetMenuItem(ctx, instanceID, in.MenuItemID)
	if errors.Is(err, store.ErrNotFound) {
		return apierr.Validation("menu_item_id is not an item of this instance's menu")
	}
	if err != nil {
		return err
	}
	if it.Hidden {
		return apierr.Validation("this menu item is hidden (oculto): show it again before sending it")
	}
	if !it.Available {
		return apierr.Validation("this menu item is marked esgotado (unavailable): mark it available before sending it")
	}
	photo := it.ImageID
	if in.MenuImageID != "" {
		photo = ""
		for _, im := range it.Images {
			if im.ID == in.MenuImageID {
				photo = im.ID
			}
		}
		if photo == "" {
			return apierr.Validation("menu_image_id is not a photo of this item")
		}
	}
	var text string
	switch in.MenuCaption {
	case "short":
		text = menu.ShortCaption(it, menu.Today())
	case "none":
	default:
		text = menu.Caption(it)
	}
	if intro := strings.TrimSpace(in.Caption); intro != "" {
		text = strings.TrimSpace(intro + "\n\n" + text)
	}
	if photo == "" {
		if text == "" {
			return apierr.Validation("this item has no photo: send it with a caption")
		}
		p.Type, p.Text = "text", text
		return nil
	}
	data, err := readMenuImage(s.DataDir, instanceID, photo)
	if err != nil {
		if text == "" || in.MenuImageID != "" {
			return apierr.Validation("the photo file of this item is missing")
		}
		p.Type, p.Text = "text", text // the photo file is gone: still send the item
		return nil
	}
	dir := filepath.Join(s.DataDir, "outbox", instanceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file := filepath.Join(dir, id)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return err
	}
	p.Type, p.Text, p.MediaFile, p.Mime, p.Size = "image", text, file, "image/jpeg", int64(len(data))
	return nil
}

// composeMenu sends the menu (or one category, menu_category_id) as one text. A menu
// that needs several messages is refused here: POST /menu/send splits it.
func (s *Messaging) composeMenu(ctx context.Context, instanceID string, in SendInput, p *queue.Payload) error {
	if in.Text != "" || in.Caption != "" || in.Media != nil {
		return apierr.Validation("type menu takes menu_category_id (optional), not text, caption or media")
	}
	if in.MenuCategoryID != "" && !validMenuID("mcat", in.MenuCategoryID) {
		return apierr.Validation("menu_category_id is the id of a menu category (GET /menu)")
	}
	m := &Menu{Store: s.Store}
	chunks, err := m.chunks(ctx, instanceID, MenuTextInput{CategoryID: in.MenuCategoryID})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return apierr.Validation("menu_category_id is not a category of this menu")
		}
		var ae *apierr.Error
		if errors.As(err, &ae) && ae.Code == "not_found" {
			return apierr.Validation("menu_category_id is not a category of this menu")
		}
		return err
	}
	switch {
	case len(chunks) == 0:
		return apierr.Validation("the menu has no available items to send")
	case len(chunks) > 1:
		return apierr.Validation(fmt.Sprintf("the menu needs %d messages: use POST /menu/send (it splits the text) or send one category (menu_category_id)",
			len(chunks)))
	}
	p.Type, p.Text = "text", chunks[0]
	return nil
}
