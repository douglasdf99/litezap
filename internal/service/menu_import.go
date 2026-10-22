package service

import (
	"context"
	"errors"
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
	// Errors lists every problem: of the input as a whole (FileErrors) and one entry per
	// error of an invalid row ("linha 3: preço inválido 'abc' ..."); ErrorDetails are the
	// row errors with their line and field.
	Errors             []string       `json:"errors"`
	FileErrors         []string       `json:"file_errors"`
	ErrorDetails       []menu.Problem `json:"error_details"`
	Valid              int            `json:"valid"`
	Invalid            int            `json:"invalid"`
	Skipped            int            `json:"skipped,omitempty"` // already in the menu (catalog copy)
	CategoriesCreated  int            `json:"categories_created"`
	CollectionsCreated int            `json:"collections_created"`
	ItemsCreated       int            `json:"items_created"`
	ImagesSaved        int            `json:"images_saved"`
	ImageErrors        []string       `json:"image_errors"`
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
	v := MenuImportView{DryRun: dry, Rows: rows, Errors: append([]string{}, errs...), FileErrors: append([]string{}, errs...),
		ErrorDetails: []menu.Problem{}, ImageErrors: []string{}}
	if v.Rows == nil {
		v.Rows = []menu.Row{}
	}
	for i := range v.Rows {
		r := &v.Rows[i]
		r.Finish()
		if len(r.Errors) == 0 {
			v.Valid++
			continue
		}
		v.Invalid++
		for _, p := range r.Problems {
			v.Errors = append(v.Errors, fmt.Sprintf("linha %d: %s", p.Line, p.Message))
			v.ErrorDetails = append(v.ErrorDetails, p)
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
		r.Line, r.Errors, r.Problems, r.Error, r.Warnings = i+1, nil, nil, "", nil
		priceErr := rowPrice(&r)
		saleErr := rowSalePrice(&r)
		menu.NormalizeRow(&r)
		r.PriceText = menu.FormatPrice(r.PriceCents, "BRL")
		r.SalePriceText = ""
		if r.SalePriceCents > 0 {
			r.SalePriceText = menu.FormatPrice(r.SalePriceCents, "BRL")
		}
		if priceErr != "" {
			r.AddError("preco", priceErr)
		}
		if saleErr != "" {
			r.AddError("preco_promocional", saleErr)
		}
		r.Check()
		if priceErr == "" && r.PriceCents == 0 {
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
		return menu.PriceError("preco", p)
	case r.PriceCents != 0 && r.PriceCents != cents:
		return "price e price_cents diferem: envie só um"
	}
	r.PriceCents = cents
	return ""
}

// rowSalePrice reads a row's sale price text (sale_price) like rowPrice.
func rowSalePrice(r *menu.Row) string {
	p := strings.TrimSpace(r.SalePrice)
	r.SalePrice = ""
	if p == "" {
		return ""
	}
	cents, err := menu.ParsePrice(p)
	switch {
	case err != nil:
		return menu.PriceError("preco_promocional", p)
	case r.SalePriceCents != 0 && r.SalePriceCents != cents:
		return "sale_price e sale_price_cents diferem: envie só um"
	}
	r.SalePriceCents = cents
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

// commitExtra is what the catalog copy adds to a commit: each row's source_ref, a
// photo fetcher (the product's photos; it returns how many it saved and why the others
// are missing) and, filled by commit, the item created for each row.
type commitExtra struct {
	refs    []string
	photo   func(ctx context.Context, row int, itemID string, budget int) (int, []string)
	itemIDs map[int]string
}

// commit saves the valid rows of v (all of them, unless skip_invalid), then fetches
// their photos (image_urls, in order; x.photo instead for the catalog copy).
func (m *Menu) commit(ctx context.Context, instanceID string, v MenuImportView, opt MenuImportOptions, x *commitExtra) (MenuImportView, error) {
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
		if x != nil && i < len(x.refs) {
			it.SourceRef = x.refs[i]
		}
		items = append(items, store.MenuImportItem{Category: r.Category, Collections: r.Collections, Item: it})
		rowOfItem = append(rowOfItem, i)
	}
	if len(items) == 0 {
		if x != nil { // nothing new, but missing photos may still be fetched
			return v, nil
		}
		return v, apierr.Validation("no valid rows to save")
	}
	res, err := m.importItems(ctx, instanceID, opt.Replace, items)
	if err != nil {
		return v, err
	}
	v.CategoriesCreated += res.CategoriesCreated
	v.CollectionsCreated += res.CollectionsCreated
	v.ItemsCreated += res.ItemsCreated
	v.Skipped += res.Skipped
	// Photos: sequential, bounded per call and in time (each one is a download).
	ictx, cancel := context.WithTimeout(ctx, importImageTime)
	defer cancel()
	for k, itemID := range res.ItemIDs {
		r := v.Rows[rowOfItem[k]]
		if itemID == "" { // skipped (already in the menu)
			continue
		}
		if x != nil {
			if x.itemIDs != nil {
				x.itemIDs[rowOfItem[k]] = itemID
			}
			if x.photo == nil {
				continue
			}
			budget := maxImportImages - v.ImagesSaved - len(v.ImageErrors)
			saved, errs := x.photo(ictx, rowOfItem[k], itemID, budget)
			v.ImagesSaved += saved
			for _, e := range errs {
				if e == errPending {
					v.ImagesPending++
					continue
				}
				v.ImageErrors = append(v.ImageErrors, fmt.Sprintf("%s: %s", r.Name, e))
			}
			continue
		}
		for _, u := range r.ImageURLs {
			if v.ImagesSaved+len(v.ImageErrors) >= maxImportImages || ictx.Err() != nil {
				v.ImagesPending++
				continue
			}
			jpg, err := m.fetchImage(ictx, MenuImageInput{URL: u})
			if err == nil {
				err = m.storeImage(ictx, instanceID, itemID, jpg, u, photoAppend)
			}
			if err != nil {
				v.ImageErrors = append(v.ImageErrors, fmt.Sprintf("%s: %s", r.Name, errMessage(err)))
				continue
			}
			v.ImagesSaved++
		}
	}
	return v, nil
}

// errPending marks a photo left for the next call (the per-call download limit).
const errPending = "\x00pending"

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
// nothing is written to WhatsApp): name, description, price and sale price,
// availability, link, retailer_id as the sku, all the photos (up to
// store.MaxMenuImages) and the catalog's collections as LiteZap collections. Products
// already copied (source "wa:<id>") are skipped; their missing photos are fetched again
// and they join the collections they are in. Hidden products and products still in
// review are left out. Photos come through the catalog's allowlisted image download.
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
	// before adding the rest (the unique source index is the last line of defense).
	// Not while previewing: a dry run writes nothing.
	if !in.DryRun {
		defer m.importLock(instanceID)()
	}
	products, err := r.Products(ctx, instanceID, store.MaxMenuItems)
	if err != nil {
		return MenuImportView{}, err
	}
	type waColl struct{ id, name string }
	var colls []waColl
	inColl := map[string][]string{} // product id -> collection names
	if cv, err := r.Collections(ctx, instanceID, ""); err == nil {
		for _, c := range cv.Collections {
			name := clipRunes(menu.CleanLine(c.Name), menu.MaxCollectionLen)
			if name == "" || (c.ReviewStatus != "" && c.ReviewStatus != "approved") {
				continue
			}
			colls = append(colls, waColl{id: c.ID, name: name})
			for _, p := range c.Products {
				if !containsStr(inColl[p.ID], name) {
					inColl[p.ID] = append(inColl[p.ID], name)
				}
			}
		}
	}
	var rows []menu.Row
	var prods []catalog.Product
	var known []store.MenuItem
	var knownProds []catalog.Product
	skipped := 0
	for _, p := range products {
		if p.Hidden || (p.ReviewStatus != "" && p.ReviewStatus != "approved") || strings.TrimSpace(p.Name) == "" {
			continue
		}
		ref := "wa:" + p.ID
		if it, err := m.Store.MenuItemBySource(ctx, instanceID, ref); err == nil {
			skipped++
			known, knownProds = append(known, it), append(knownProds, p)
			continue
		}
		cents := (p.PriceAmount1000 + 5) / 10
		sale := int64(0)
		if p.SaleAmount1000 > 0 && p.SaleAmount1000 < p.PriceAmount1000 {
			sale = (p.SaleAmount1000 + 5) / 10
		}
		cat := catalogCategory
		if names := inColl[p.ID]; len(names) > 0 {
			cat = names[0]
		}
		link := ""
		if u := strings.TrimSpace(p.URL); menu.ValidLinkURL(u) {
			link = u
		}
		row := menu.Row{Line: len(rows) + 1, Category: clipRunes(cat, menu.MaxCategoryLen), Name: clipRunes(p.Name, menu.MaxNameLen),
			Description: clipRunes(p.Description, menu.MaxDescriptionLen), PriceCents: cents, SalePriceCents: sale,
			Available: p.Availability != "out_of_stock", SKU: clipRunes(menu.CleanLine(p.RetailerID), menu.MaxSKULen), LinkURL: link,
			Collections: inColl[p.ID]}
		menu.NormalizeRow(&row)
		row.PriceText = menu.FormatPrice(row.PriceCents, "BRL")
		if row.SalePriceCents > 0 {
			row.SalePriceText = menu.FormatPrice(row.SalePriceCents, "BRL")
		}
		row.Check()
		rows = append(rows, row)
		prods = append(prods, p)
	}
	v := newImportView(rows, nil, in.DryRun)
	v.Skipped = skipped
	if in.DryRun {
		return v, nil
	}
	// The catalog's collections become LiteZap collections (by source, so a second copy
	// finds them even when renamed here).
	collID := map[string]string{}
	for _, c := range colls {
		ref := "wa:" + c.id
		if cur, err := m.Store.MenuCollectionBySource(ctx, instanceID, ref); err == nil {
			collID[c.name] = cur.ID
			continue
		}
		created, err := m.Store.CreateMenuCollection(ctx, instanceID, store.MenuCollection{Name: c.name, Active: true, SourceRef: ref})
		switch {
		case errors.Is(err, store.ErrMenuLimit):
			v.Errors = append(v.Errors, fmt.Sprintf("coleção %q não copiada: o cardápio tem no máximo %d coleções", c.name, store.MaxMenuCollections))
			continue
		case err != nil:
			return v, menuErr(err, "menu collection")
		}
		collID[c.name] = created.ID
		v.CollectionsCreated++
	}
	for i := range rows { // rows join the copied collections by id, not by name
		rows[i].Collections = nil
	}
	v.Rows = rows
	images := in.Images == nil || *in.Images
	x := &commitExtra{itemIDs: map[int]string{}}
	for _, p := range prods {
		x.refs = append(x.refs, "wa:"+p.ID)
	}
	if images {
		x.photo = func(ctx context.Context, row int, itemID string, budget int) (int, []string) {
			return m.catalogPhotos(ctx, instanceID, inst, prods[row], itemID, 0, budget)
		}
	}
	itemOf := map[string]string{} // product id -> item id
	if len(rows) > 0 {
		v, err = m.commit(ctx, instanceID, v, MenuImportOptions{SkipInvalid: true}, x)
		if err != nil {
			return v, err
		}
		for row, id := range x.itemIDs {
			itemOf[prods[row].ID] = id
		}
	}
	for i, it := range known {
		itemOf[knownProds[i].ID] = it.ID
	}
	for _, p := range products { // in the catalog's order
		item, ok := itemOf[p.ID]
		if !ok {
			continue
		}
		for _, n := range inColl[p.ID] {
			if cid := collID[n]; cid != "" {
				if err := m.Store.AddMenuCollectionItems(ctx, instanceID, cid, []string{item}); errors.Is(err, store.ErrMenuLimit) {
					v.Errors = append(v.Errors, fmt.Sprintf("coleção %q cheia (%d itens): %q ficou de fora", n, store.MaxCollectionItems, p.Name))
				} else if err != nil {
					return v, menuErr(err, "menu collection")
				}
			}
		}
	}
	if images {
		for i, it := range known {
			if len(it.Images) >= len(knownProds[i].Images) || len(it.Images) >= store.MaxMenuImages {
				continue
			}
			budget := maxImportImages - v.ImagesSaved - len(v.ImageErrors)
			saved, errs := m.catalogPhotos(ctx, instanceID, inst, knownProds[i], it.ID, len(it.Images), budget)
			v.ImagesSaved += saved
			for _, e := range errs {
				if e == errPending {
					v.ImagesPending++
				} else {
					v.ImageErrors = append(v.ImageErrors, fmt.Sprintf("%s: %s", it.Name, e))
				}
			}
		}
	}
	return v, nil
}

// catalogPhotos copies a product's photos from index from on (the ones the item does
// not have yet) into a menu item, at most budget downloads; the rest are errPending.
func (m *Menu) catalogPhotos(ctx context.Context, instanceID string, inst store.Instance, p catalog.Product, itemID string,
	from, budget int) (int, []string) {
	saved := 0
	var errs []string
	for i := from; i < len(p.Images) && i < store.MaxMenuImages; i++ {
		if saved+len(errs) >= budget || ctx.Err() != nil {
			errs = append(errs, errPending)
			continue
		}
		data, _, err := m.Catalog.imageAt(ctx, instanceID, inst, p, i)
		var jpg []byte
		if err == nil {
			jpg, err = m.normalize(ctx, data)
		}
		if err == nil {
			err = m.storeImage(ctx, instanceID, itemID, jpg, "", photoAppend)
		}
		if err != nil {
			errs = append(errs, errMessage(err))
			continue
		}
		saved++
	}
	return saved, errs
}

func clipRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
