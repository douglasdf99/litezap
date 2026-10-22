package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/catalog"
	"github.com/douglasdf99/litezap/internal/menu"
	"github.com/douglasdf99/litezap/internal/store"
)

// ---- menu imports: spreadsheet, WhatsApp catalog (read only) and "Montar com IA" ----

// MenuImportView is a preview (dry_run) or the outcome of an import.
type MenuImportView struct {
	DryRun bool       `json:"dry_run"`
	Rows   []menu.Row `json:"rows"`
	// Errors are problems of the input as a whole (the rows carry their own).
	Errors            []string `json:"errors"`
	Valid             int      `json:"valid"`
	Invalid           int      `json:"invalid"`
	Skipped           int      `json:"skipped,omitempty"` // already in the menu (catalog copy)
	CategoriesCreated int      `json:"categories_created"`
	ItemsCreated      int      `json:"items_created"`
	ImagesSaved       int      `json:"images_saved"`
	ImageErrors       []string `json:"image_errors"`
	// ImagesPending are photos not fetched in this call (limit per call): run it again.
	ImagesPending int `json:"images_pending,omitempty"`
}

// MenuImportOptions control a commit.
type MenuImportOptions struct {
	DryRun bool `json:"dry_run,omitempty"`
	// Replace empties the menu first (default: add to it).
	Replace bool `json:"replace,omitempty"`
	// SkipInvalid saves the valid rows when some are invalid (default: refuse).
	SkipInvalid bool `json:"skip_invalid,omitempty"`
}

// MenuCSVInput is a spreadsheet exported as CSV.
type MenuCSVInput struct {
	CSV string `json:"csv"`
	MenuImportOptions
}

// MenuRowsInput saves reviewed rows (the confirm step of "Montar com IA").
type MenuRowsInput struct {
	Rows []menu.Row `json:"rows"`
	MenuImportOptions
}

// MenuAIInput is the free text of "Montar com IA".
type MenuAIInput struct {
	Text string `json:"text"`
}

func newImportView(rows []menu.Row, errs []string, dry bool) MenuImportView {
	v := MenuImportView{DryRun: dry, Rows: rows, Errors: errs, ImageErrors: []string{}}
	if v.Rows == nil {
		v.Rows = []menu.Row{}
	}
	if v.Errors == nil {
		v.Errors = []string{}
	}
	for _, r := range rows {
		if len(r.Errors) == 0 {
			v.Valid++
		} else {
			v.Invalid++
		}
	}
	return v
}

// ImportCSV previews (dry_run) or saves a spreadsheet.
func (m *Menu) ImportCSV(ctx context.Context, instanceID string, in MenuCSVInput) (MenuImportView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuImportView{}, err
	}
	res := menu.ParseCSV(in.CSV)
	v := newImportView(res.Rows, res.Errors, in.DryRun)
	if in.DryRun {
		return v, nil
	}
	if len(res.Errors) > 0 && len(res.Rows) == 0 {
		return v, apierr.Validation("the spreadsheet could not be read: " + strings.Join(res.Errors, "; "))
	}
	return m.commit(ctx, instanceID, v, in.MenuImportOptions, nil)
}

// ImportRows saves reviewed rows, validated again here (they come from the browser).
func (m *Menu) ImportRows(ctx context.Context, instanceID string, in MenuRowsInput) (MenuImportView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuImportView{}, err
	}
	if len(in.Rows) == 0 || len(in.Rows) > store.MaxMenuItems {
		return MenuImportView{}, apierr.Validation(fmt.Sprintf("rows lists 1 to %d items", store.MaxMenuItems))
	}
	rows := make([]menu.Row, 0, len(in.Rows))
	for i, r := range in.Rows {
		r.Line, r.Errors, r.Warnings = i+1, nil, nil
		priceErr := rowPrice(&r)
		menu.NormalizeRow(&r)
		r.PriceText = menu.FormatPrice(r.PriceCents, "BRL")
		r.Errors = menu.ValidateRow(r)
		if priceErr != "" {
			r.Errors = append(r.Errors, priceErr)
		} else if r.PriceCents == 0 {
			r.Warnings = append(r.Warnings, "sem preço (R$ 0,00): confirme o valor antes de salvar")
		}
		rows = append(rows, r)
	}
	v := newImportView(rows, nil, in.DryRun)
	if in.DryRun {
		return v, nil
	}
	return m.commit(ctx, instanceID, v, in.MenuImportOptions, nil)
}

// rowPrice reads a row's price text (price) into price_cents and clears it; it returns
// the row's price problem, if any.
func rowPrice(r *menu.Row) string {
	p := strings.TrimSpace(r.Price)
	r.Price = ""
	if p == "" {
		return ""
	}
	cents, err := menu.ParsePrice(p)
	switch {
	case err != nil:
		return fmt.Sprintf("preço %q não reconhecido (use 45,90 ou R$ 1.234,50)", clipRunes(p, 40))
	case r.PriceCents != 0 && r.PriceCents != cents:
		return "price e price_cents diferem: envie só um"
	}
	r.PriceCents = cents
	return ""
}

// importItems checks the item limit and imports. A replace holds the instance's import
// lock (it empties the menu, so it must not interleave with a catalog copy).
func (m *Menu) importItems(ctx context.Context, instanceID string, replace bool, items []store.MenuImportItem) (store.MenuImportResult, error) {
	if replace {
		defer m.importLock(instanceID)()
	}
	_, n, err := m.Store.MenuCounts(ctx, instanceID)
	if err != nil {
		return store.MenuImportResult{}, err
	}
	if replace {
		n = 0
	}
	if n+len(items) > store.MaxMenuItems {
		return store.MenuImportResult{}, apierr.New(http.StatusConflict, "menu_limit", fmt.Sprintf("the menu has %d items and this import adds %d: the limit is %d",
			n, len(items), store.MaxMenuItems), false)
	}
	res, err := m.Store.ImportMenu(ctx, instanceID, replace, items)
	if err != nil {
		return res, menuErr(err, "menu")
	}
	for _, img := range res.RemovedImages {
		m.removeImage(instanceID, img)
	}
	return res, nil
}

// commit saves the valid rows of v (all of them, unless skip_invalid), then fetches
// their image_url photos. fill, when set, fetches extra photos after the import (the
// catalog copy: products already in the menu without a photo).
func (m *Menu) commit(ctx context.Context, instanceID string, v MenuImportView, opt MenuImportOptions,
	photo func(ctx context.Context, row int, itemID string) error) (MenuImportView, error) {
	if v.Invalid > 0 && !opt.SkipInvalid {
		return v, apierr.Validation(fmt.Sprintf("%d rows have errors: fix them or send skip_invalid", v.Invalid))
	}
	var items []store.MenuImportItem
	var rowOfItem []int
	for i, r := range v.Rows {
		if len(r.Errors) > 0 {
			continue
		}
		it := menu.RowToItem(r)
		items = append(items, store.MenuImportItem{Category: r.Category, Item: it})
		rowOfItem = append(rowOfItem, i)
	}
	if len(items) == 0 {
		if photo != nil { // nothing new, but missing photos may still be fetched
			return v, nil
		}
		return v, apierr.Validation("no valid rows to save")
	}
	res, err := m.importItems(ctx, instanceID, opt.Replace, items)
	if err != nil {
		return v, err
	}
	v.CategoriesCreated, v.ItemsCreated = res.CategoriesCreated, res.ItemsCreated
	v.Skipped += res.Skipped
	// Photos: sequential, bounded per call and in time (each one is a download).
	ictx, cancel := context.WithTimeout(ctx, importImageTime)
	defer cancel()
	for k, itemID := range res.ItemIDs {
		r := v.Rows[rowOfItem[k]]
		if itemID == "" || (r.ImageURL == "" && photo == nil) { // skipped (already in the menu)
			continue
		}
		if v.ImagesSaved+len(v.ImageErrors) >= maxImportImages || ictx.Err() != nil {
			v.ImagesPending++
			continue
		}
		var err error
		if photo != nil {
			err = photo(ictx, rowOfItem[k], itemID)
		} else {
			var jpg []byte
			if jpg, err = m.fetchImage(ictx, MenuImageInput{URL: r.ImageURL}); err == nil {
				err = m.storeImage(ictx, instanceID, itemID, jpg)
			}
		}
		switch {
		case err == errNoPhoto:
		case err != nil:
			v.ImageErrors = append(v.ImageErrors, fmt.Sprintf("%s: %s", r.Name, errMessage(err)))
		default:
			v.ImagesSaved++
		}
	}
	return v, nil
}

var errNoPhoto = apierr.New(http.StatusNotFound, "not_found", "no photo", false)

// BuildWithAI turns a free text into rows for review with the configured AI provider.
// Nothing is saved: the user reviews the rows and confirms with ImportRows.
func (m *Menu) BuildWithAI(ctx context.Context, instanceID string, in MenuAIInput) (MenuImportView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return MenuImportView{}, err
	}
	if m.AI == nil {
		return MenuImportView{}, apierr.New(http.StatusNotImplemented, "not_implemented", "AI is not available on this server", false)
	}
	text := strings.TrimSpace(in.Text)
	if text == "" || utf8.RuneCountInString(text) > menu.MaxAIText {
		return MenuImportView{}, apierr.Validation(fmt.Sprintf("text is required (at most %d characters)", menu.MaxAIText))
	}
	if !m.aiQuota.take(instanceID, time.Now(), menuAIQuota...) {
		return MenuImportView{}, socialLimited("\"Montar com IA\"")
	}
	system, user := menu.AIPrompt(text)
	out, err := m.AI(ctx, instanceID, system, user, menu.AISchema)
	if err != nil {
		return MenuImportView{}, err
	}
	rows, err := menu.ParseAI(out, text)
	if err != nil {
		return MenuImportView{}, apierr.New(http.StatusUnprocessableEntity, "ai_invalid_output", err.Error(), true)
	}
	return newImportView(rows, nil, true), nil
}

// MenuCatalogInput copies the WhatsApp catalog.
type MenuCatalogInput struct {
	DryRun bool `json:"dry_run,omitempty"`
	// Images fetches the products' photos (default true).
	Images *bool `json:"images,omitempty"`
}

const catalogCategory = "Catálogo do WhatsApp"

// ImportCatalog copies the instance's own WhatsApp catalog into the menu (read only:
// nothing is written to WhatsApp). Products already copied (sku "wa:<id>") are skipped;
// their missing photos are fetched again. Hidden products and products still in review
// are left out. Photos come through the catalog's allowlisted image download.
func (m *Menu) ImportCatalog(ctx context.Context, instanceID string, in MenuCatalogInput) (MenuImportView, error) {
	inst, err := m.instance(ctx, instanceID)
	if err != nil {
		return MenuImportView{}, err
	}
	r := m.Catalog
	if r == nil || r.Client == nil {
		return MenuImportView{}, apierr.New(http.StatusNotImplemented, "not_implemented", "the WhatsApp catalog is not available on this server", false)
	}
	// One copy at a time per instance: it reads which products are already in the menu
	// before adding the rest (the unique "wa:" sku index is the last line of defense).
	// Not while previewing: a dry run writes nothing.
	if !in.DryRun {
		defer m.importLock(instanceID)()
	}
	products, err := r.Products(ctx, instanceID, store.MaxMenuItems)
	if err != nil {
		return MenuImportView{}, err
	}
	coll := map[string]string{}
	if cv, err := r.Collections(ctx, instanceID, ""); err == nil {
		for _, c := range cv.Collections {
			for _, p := range c.Products {
				if _, ok := coll[p.ID]; !ok && strings.TrimSpace(c.Name) != "" {
					coll[p.ID] = c.Name
				}
			}
		}
	}
	var rows []menu.Row
	var prods []catalog.Product
	var missingPhotos []store.MenuItem
	var missingProds []catalog.Product
	skipped := 0
	for _, p := range products {
		if p.Hidden || (p.ReviewStatus != "" && p.ReviewStatus != "approved") || strings.TrimSpace(p.Name) == "" {
			continue
		}
		sku := "wa:" + p.ID
		if it, err := m.Store.MenuItemBySKU(ctx, instanceID, sku); err == nil {
			skipped++
			if it.ImageID == "" && len(p.Images) > 0 {
				missingPhotos, missingProds = append(missingPhotos, it), append(missingProds, p)
			}
			continue
		}
		cents := (p.PriceAmount1000 + 5) / 10
		if p.SaleAmount1000 > 0 && p.SaleAmount1000 < p.PriceAmount1000 {
			cents = (p.SaleAmount1000 + 5) / 10
		}
		cat := coll[p.ID]
		if cat == "" {
			cat = catalogCategory
		}
		row := menu.Row{Line: len(rows) + 1, Category: clipRunes(cat, menu.MaxCategoryLen), Name: clipRunes(p.Name, menu.MaxNameLen),
			Description: clipRunes(p.Description, menu.MaxDescriptionLen), PriceCents: cents, Available: p.Availability != "out_of_stock", SKU: sku}
		menu.NormalizeRow(&row)
		row.PriceText = menu.FormatPrice(row.PriceCents, "BRL")
		row.Errors = menu.ValidateRow(row)
		rows = append(rows, row)
		prods = append(prods, p)
	}
	v := newImportView(rows, nil, in.DryRun)
	v.Skipped = skipped
	if in.DryRun {
		return v, nil
	}
	images := in.Images == nil || *in.Images
	var photo func(ctx context.Context, row int, itemID string) error
	if images {
		photo = func(ctx context.Context, row int, itemID string) error {
			return m.catalogPhoto(ctx, instanceID, inst, prods[row], itemID)
		}
	}
	if len(rows) > 0 {
		v, err = m.commit(ctx, instanceID, v, MenuImportOptions{SkipInvalid: true}, photo)
		if err != nil {
			return v, err
		}
	}
	if images {
		for i, it := range missingPhotos {
			if v.ImagesSaved+len(v.ImageErrors) >= maxImportImages || ctx.Err() != nil {
				v.ImagesPending++
				continue
			}
			if err := m.catalogPhoto(ctx, instanceID, inst, missingProds[i], it.ID); err != nil && err != errNoPhoto {
				v.ImageErrors = append(v.ImageErrors, fmt.Sprintf("%s: %s", it.Name, errMessage(err)))
			} else if err == nil {
				v.ImagesSaved++
			}
		}
	}
	return v, nil
}

// catalogPhoto copies a product's first photo into a menu item.
func (m *Menu) catalogPhoto(ctx context.Context, instanceID string, inst store.Instance, p catalog.Product, itemID string) error {
	if len(p.Images) == 0 {
		return errNoPhoto
	}
	data, _, err := m.Catalog.image(ctx, instanceID, inst, p)
	if err != nil {
		return err
	}
	jpg, err := m.normalize(ctx, data)
	if err != nil {
		return err
	}
	return m.storeImage(ctx, instanceID, itemID, jpg)
}

func clipRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
