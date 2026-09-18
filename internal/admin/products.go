// products.go implements Wave 4 Task 2 (Товары): the product list, the
// create/edit form, and the CSV/Excel import page — replacing the stub
// handlers Foundation (Task 1) registered for these screens. Every handler
// here calls internal/catalog's repositories directly (ProductRepo,
// VariantRepo, ImageRepo, StockRepo, CategoryRepo), the same in-process
// pattern internal/web's product_actions.go/catalog_view.go already use —
// never an HTTP call to this codebase's own /admin/api/* JSON endpoints.
//
// The one deliberate exception is the import page's actual upload: the
// exact route this task would otherwise register, POST
// /admin/products/import, already exists — httpapi.RegisterAdminImportRoutes
// mounts it (wired into cmd/server/main.go ahead of this task landing), and
// net/http.ServeMux panics on a second handler for an identical
// method+pattern. So productImportPage's own page just renders an upload
// form whose JS POSTs the file straight to that existing JSON endpoint (a
// real browser request, not a Go-to-Go round trip) and renders the
// {imported, errors} response client-side — see productImportPage's doc
// comment below for the full reasoning.
package admin

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// productsShellData builds the PageData common to every Task 2 screen —
// like handlers.shellPageData, but decoupled from the sidebar's active-nav
// key: the form and import screens render under their own Screen
// ("product_form"/"product_import", selecting the right .gohtml content)
// while still highlighting the "Товары" nav item, so navItemsForRole is
// always called with "products" here regardless of which of the three
// screens is actually rendering.
func (h *handlers) productsShellData(screen, title string, st *staff.Staff) PageData {
	return PageData{
		Screen:      screen,
		PageTitle:   title,
		ShowSidebar: true,
		Staff:       st,
		Initials:    initialsFor(st.Name),
		RoleLabel:   roleLabel(st.Role),
		NavItems:    navItemsForRole(st.Role, "products"),
	}
}

// photoURL builds a direct (non-presigned) URL to objectKey in the
// cozy-media bucket — the exact same scheme+endpoint+bucket construction
// internal/web's handlers.photoURL (catalog_view.go) already uses for the
// public storefront, so admin thumbnails and product-page photos resolve
// to the same public-read bucket URL.
func (h *handlers) photoURL(objectKey string) string {
	if objectKey == "" {
		return ""
	}
	scheme := "http"
	if h.cfg.MinIOUseSSL {
		scheme = "https"
	}
	return scheme + "://" + h.cfg.MinIOEndpoint + "/" + h.cfg.MinIOBucket + "/" + objectKey
}

// renderInternalErr is the fallback for an unexpected (non-apperr, e.g. a
// dropped DB connection) error from a repo call — logging is left to the
// standard http.Error/500 path other admin handlers already use
// (loginSubmit, stubPage) rather than introducing a new logging
// convention just for this task.
func (h *handlers) renderInternalErr(w http.ResponseWriter, err error) {
	http.Error(w, "внутренняя ошибка", http.StatusInternalServerError)
}

// redirectWithToast redirects to path with ?toast=<message> appended
// (picked up by productsListPage/productEditPage via PageData.Toast), or —
// for a request HTMX issued (the delete confirm modal's hx-post) — sets
// HX-Redirect instead so htmx performs a full client-side navigation
// rather than trying to swap the redirect's HTML into whatever element
// triggered it, mirroring internal/web's redirectToLogin.
func redirectWithToast(w http.ResponseWriter, r *http.Request, path, toast string) {
	target := path
	if toast != "" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		target = path + sep + "toast=" + url.QueryEscape(toast)
	}
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// --- list screen ---

// productsListPage handles GET /admin/products: the category/subcategory
// chip filters, search box, and the desktop table / mobile cards
// (products.gohtml handles the table-vs-cards split with a pure CSS media
// query, not a server-side device check).
func (h *handlers) productsListPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)

	tree, err := h.categories.Tree(ctx)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}

	query := r.URL.Query()
	q := strings.TrimSpace(query.Get("q"))
	catSlug := query.Get("cat")
	subSlug := query.Get("sub")
	page := parsePositiveInt(query.Get("page"), 1)
	pageSize := parsePositiveInt(query.Get("page_size"), 25)
	if pageSize != 25 && pageSize != 50 {
		pageSize = 25
	}

	categoryIDs, activeTop, activeSub := resolveCategoryFilter(tree, catSlug, subSlug)

	products, total, err := h.products.ListForAdmin(ctx, catalog.AdminListFilter{
		CategoryIDs: categoryIDs,
		Query:       q,
		Page:        page,
		PageSize:    pageSize,
	})
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}

	productIDs := make([]string, len(products))
	for i, p := range products {
		productIDs[i] = p.ID
	}

	images, err := h.images.PrimaryForProducts(ctx, productIDs)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}
	variantCounts, err := h.variants.CountByProductIDs(ctx, productIDs)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}
	stockTotals, err := h.stock.TotalByProductIDs(ctx, productIDs)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}

	rows := make([]ProductRowVM, len(products))
	for i, p := range products {
		rows[i] = h.buildProductRow(p, tree, images, variantCounts, stockTotals)
	}

	pageCount := (total + pageSize - 1) / pageSize
	if pageCount < 1 {
		pageCount = 1
	}

	data := ProductsPageData{
		CanEdit:       true, // route is already ownerOrManager-gated; see routes.go
		CanDelete:     st.Role == staff.RoleOwner,
		CategoryChips: buildCategoryChips(tree, activeTop, q),
		ShowSubs:      activeTop != nil && len(activeTop.Children) > 0,
		SubChips:      buildSubChips(activeTop, activeSub, q),
		Products:      rows,
		Empty:         len(rows) == 0,
		CountLabel:    countLabel(total),
		Query:         q,
		PageSize:      pageSize,
		Page:          page,
		PageCount:     pageCount,
		NewURL:        "/admin/products/new",
		ImportURL:     "/admin/products/import",
	}

	pageData := h.productsShellData("products", "Товары", st)
	pageData.ShowSearch = true
	pageData.SearchQuery = q
	pageData.Toast = query.Get("toast")
	pageData.Data = data

	if err := h.render.Render(w, "products", pageData); err != nil {
		http.Error(w, "ошибка рендеринга страницы", http.StatusInternalServerError)
	}
}

// buildProductRow shapes one catalog.Product plus its batch-looked-up
// image/variant-count/stock-total into the row the list template renders.
func (h *handlers) buildProductRow(p catalog.Product, tree []*catalog.Category, images map[string]catalog.ProductImage, variantCounts, stockTotals map[string]int) ProductRowVM {
	img, hasPhoto := images[p.ID]
	stockLabel, fg, bg := stockChip(stockTotals[p.ID])

	return ProductRowVM{
		ID:              p.ID,
		Name:            p.NameRu,
		Brand:           stringOrEmpty(p.Brand),
		CategoryPath:    categoryPath(tree, p.CategoryID),
		PriceText:       formatMoney(p.BasePrice),
		VariantsLabel:   variantsLabel(variantCounts[p.ID]),
		StockLabel:      stockLabel,
		StockFG:         fg,
		StockBG:         bg,
		StatusLabel:     statusLabel(p.IsActive),
		HasPhoto:        hasPhoto,
		PhotoURL:        h.photoURL(img.ObjectKey),
		EditURL:         "/admin/products/" + p.ID,
		ToggleActiveURL: "/admin/products/" + p.ID + "/toggle-active",
		DeactivateLabel: deactivateLabel(p.IsActive),
		DeleteURL:       "/admin/products/" + p.ID + "/delete",
	}
}

func stringOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func parsePositiveInt(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return def
	}
	return n
}

// --- create/edit form ---

// productNewPage handles GET /admin/products/new: an empty form.
func (h *handlers) productNewPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)

	tree, err := h.categories.Tree(ctx)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}

	brands, err := h.products.DistinctBrands(ctx)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}

	options := buildCategoryOptions(tree)
	data := ProductFormData{
		Categories:     options,
		CategoriesJSON: categoryOptionsJSON(options),
		Brands:         buildBrandOptions(brands),
		CanDelete:      st.Role == staff.RoleOwner,
	}
	h.renderProductForm(w, st, "Новый товар", data)
}

// productEditPage handles GET /admin/products/{id}: the form pre-filled
// from the product, its variants (with each variant's current total stock
// summed across every point of sale — see sumStockByVariant), and its
// existing photos.
func (h *handlers) productEditPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)
	id := r.PathValue("id")

	product, err := h.products.GetByIDAny(ctx, id)
	if err != nil {
		redirectWithToast(w, r, "/admin/products", appErrMessage(err))
		return
	}

	tree, err := h.categories.Tree(ctx)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}

	variants, err := h.variants.ListByProduct(ctx, id)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}
	variantIDs := make([]string, len(variants))
	for i, v := range variants {
		variantIDs[i] = v.ID
	}
	stockEntries, err := h.stock.ByVariantIDs(ctx, variantIDs)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}
	stockByVariant := sumStockByVariant(stockEntries)

	images, err := h.images.ListByProduct(ctx, id)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}

	brands, err := h.products.DistinctBrands(ctx)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}

	topID, subID := resolveTopAndSubIDs(tree, product.CategoryID)

	variantRows := make([]VariantRowVM, len(variants))
	for i, v := range variants {
		qty := stockByVariant[v.ID]
		label, fg, bg := stockChip(qty)
		variantRows[i] = VariantRowVM{ID: v.ID, Size: v.Size, Color: v.Color, Qty: qty, BadgeLbl: label, BadgeFG: fg, BadgeBG: bg}
	}

	imageRows := make([]ImageRowVM, len(images))
	for i, img := range images {
		imageRows[i] = ImageRowVM{ObjectKey: img.ObjectKey, URL: h.photoURL(img.ObjectKey)}
	}

	options := buildCategoryOptions(tree)
	data := ProductFormData{
		IsEdit:         true,
		ProductID:      product.ID,
		NameRu:         product.NameRu,
		NameKy:         product.NameKy,
		CategoryID:     product.CategoryID,
		TopCategoryID:  topID,
		SubCategoryID:  subID,
		Brand:          stringOrEmpty(product.Brand),
		BasePrice:      strconv.FormatFloat(product.BasePrice, 'f', -1, 64),
		DescriptionRu:  stringOrEmpty(product.DescriptionRu),
		DescriptionKy:  stringOrEmpty(product.DescriptionKy),
		Categories:     options,
		CategoriesJSON: categoryOptionsJSON(options),
		Brands:         buildBrandOptions(brands),
		Images:         imageRows,
		Variants:       variantRows,
		CanDelete:      st.Role == staff.RoleOwner,
	}
	h.renderProductForm(w, st, "Редактирование товара", data)
}

func sumStockByVariant(entries []catalog.StockEntry) map[string]int {
	out := make(map[string]int, len(entries))
	for _, e := range entries {
		out[e.VariantID] += e.Quantity
	}
	return out
}

func (h *handlers) renderProductForm(w http.ResponseWriter, st *staff.Staff, title string, data ProductFormData) {
	pageData := h.productsShellData("product_form", title, st)
	pageData.ShowBack = true
	pageData.Data = data
	if err := h.render.Render(w, "product_form", pageData); err != nil {
		http.Error(w, "ошибка рендеринга страницы", http.StatusInternalServerError)
	}
}

// productCreate handles POST /admin/products.
func (h *handlers) productCreate(w http.ResponseWriter, r *http.Request) {
	h.saveProduct(w, r, "")
}

// productUpdate handles POST /admin/products/{id}.
func (h *handlers) productUpdate(w http.ResponseWriter, r *http.Request) {
	h.saveProduct(w, r, r.PathValue("id"))
}

// saveProduct backs both productCreate and productUpdate: it upserts the
// product row itself, then its variants (create/update/delete-by-diff) and
// stock, then its photo set — in that order, so a failure partway through
// (e.g. an invalid variant) still leaves the product record saved rather
// than losing the whole submission, and redirects back to the edit page
// with a toast explaining what didn't apply instead of silently dropping
// it.
func (h *handlers) saveProduct(w http.ResponseWriter, r *http.Request, productID string) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)

	if err := r.ParseForm(); err != nil {
		h.rerenderFormOnError(w, r, st, productID, "не удалось прочитать форму")
		return
	}

	isActive := true
	if productID != "" {
		current, err := h.products.GetByIDAny(ctx, productID)
		if err != nil {
			redirectWithToast(w, r, "/admin/products", appErrMessage(err))
			return
		}
		isActive = current.IsActive
	}

	input := catalog.ProductInput{
		CategoryID:    r.FormValue("category_id"),
		NameRu:        r.FormValue("name_ru"),
		NameKy:        r.FormValue("name_ky"),
		DescriptionRu: nilIfEmpty(r.FormValue("description_ru")),
		DescriptionKy: nilIfEmpty(r.FormValue("description_ky")),
		Brand:         nilIfEmpty(r.FormValue("brand")),
		BasePrice:     parsePrice(r.FormValue("base_price")),
		IsActive:      isActive,
	}

	var product *catalog.Product
	var err error
	if productID == "" {
		product, err = h.products.Create(ctx, input)
	} else {
		product, err = h.products.Update(ctx, productID, input)
	}
	if err != nil {
		h.rerenderFormOnError(w, r, st, productID, appErrMessage(err))
		return
	}

	if err := h.saveVariants(ctx, product.ID, r); err != nil {
		redirectWithToast(w, r, "/admin/products/"+product.ID, "Товар сохранён, но вариации: "+appErrMessage(err))
		return
	}
	if err := h.saveImages(ctx, product.ID, r); err != nil {
		redirectWithToast(w, r, "/admin/products/"+product.ID, "Товар сохранён, но фото: "+appErrMessage(err))
		return
	}

	redirectWithToast(w, r, "/admin/products", "Товар сохранён")
}

// rerenderFormOnError redisplays the form with whatever the staff member
// had just submitted (rebuilt from r.Form, not re-read from the database)
// plus an error message, instead of losing their input on a validation
// failure (e.g. an invalid category).
func (h *handlers) rerenderFormOnError(w http.ResponseWriter, r *http.Request, st *staff.Staff, productID, errMsg string) {
	tree, err := h.categories.Tree(r.Context())
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}
	brands, err := h.products.DistinctBrands(r.Context())
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}

	categoryID := r.FormValue("category_id")
	topID, subID := resolveTopAndSubIDs(tree, categoryID)

	sizes := r.Form["variant_size"]
	colors := r.Form["variant_color"]
	qtys := r.Form["variant_qty"]
	ids := r.Form["variant_id"]
	variantRows := make([]VariantRowVM, 0, len(sizes))
	for i := range sizes {
		size := strings.TrimSpace(formAt(sizes, i))
		color := strings.TrimSpace(formAt(colors, i))
		if size == "" && color == "" {
			continue
		}
		qty := parseQty(formAt(qtys, i))
		label, fg, bg := stockChip(qty)
		variantRows = append(variantRows, VariantRowVM{ID: formAt(ids, i), Size: size, Color: color, Qty: qty, BadgeLbl: label, BadgeFG: fg, BadgeBG: bg})
	}

	keys := r.Form["image_object_key"]
	imageRows := make([]ImageRowVM, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		imageRows = append(imageRows, ImageRowVM{ObjectKey: k, URL: h.photoURL(k)})
	}

	formOptions := buildCategoryOptions(tree)
	data := ProductFormData{
		IsEdit:         productID != "",
		ProductID:      productID,
		NameRu:         r.FormValue("name_ru"),
		NameKy:         r.FormValue("name_ky"),
		CategoryID:     categoryID,
		TopCategoryID:  topID,
		SubCategoryID:  subID,
		Brand:          r.FormValue("brand"),
		BasePrice:      r.FormValue("base_price"),
		DescriptionRu:  r.FormValue("description_ru"),
		DescriptionKy:  r.FormValue("description_ky"),
		Categories:     formOptions,
		CategoriesJSON: categoryOptionsJSON(formOptions),
		Brands:         buildBrandOptions(brands),
		Images:         imageRows,
		Variants:       variantRows,
		CanDelete:      st.Role == staff.RoleOwner,
		Err:            errMsg,
	}
	title := "Новый товар"
	if data.IsEdit {
		title = "Редактирование товара"
	}
	h.renderProductForm(w, st, title, data)
}

// saveVariants diffs the submitted variant_id[]/variant_size[]/
// variant_color[]/variant_qty[] arrays against productID's existing
// variants: a row with a known id is updated, a row with no id (or an id
// that isn't actually one of this product's variants) is created, and any
// existing variant whose id wasn't present in the submission at all is
// deleted — the whole variants table is always submitted as one unit, the
// same "always submit the full set" approach catalog.ImageRepo.
// ReplaceForProduct already uses for photos. Each surviving row's quantity
// is then upserted at the shop's default point of sale (resolveDefaultPointID) —
// see that function's doc comment for why a single point.
func (h *handlers) saveVariants(ctx context.Context, productID string, r *http.Request) error {
	sizes := r.Form["variant_size"]
	colors := r.Form["variant_color"]
	qtys := r.Form["variant_qty"]
	ids := r.Form["variant_id"]

	existing, err := h.variants.ListByProduct(ctx, productID)
	if err != nil {
		return err
	}
	existingByID := make(map[string]bool, len(existing))
	for _, v := range existing {
		existingByID[v.ID] = true
	}

	pointID, havePoint := h.resolveDefaultPointID(ctx)

	seen := make(map[string]bool, len(sizes))
	for i := range sizes {
		size := strings.TrimSpace(formAt(sizes, i))
		color := strings.TrimSpace(formAt(colors, i))
		if size == "" && color == "" {
			continue
		}
		qty := parseQty(formAt(qtys, i))
		in := catalog.VariantInput{Size: size, Color: color}

		id := formAt(ids, i)
		var variantID string
		if id != "" && existingByID[id] {
			if _, err := h.variants.Update(ctx, id, in); err != nil {
				return err
			}
			variantID = id
		} else {
			v, err := h.variants.Create(ctx, productID, in)
			if err != nil {
				return err
			}
			variantID = v.ID
		}
		seen[variantID] = true

		if havePoint {
			if _, err := h.stock.Upsert(ctx, variantID, pointID, qty); err != nil {
				return err
			}
		}
	}

	for _, v := range existing {
		if !seen[v.ID] {
			if err := h.variants.Delete(ctx, v.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveDefaultPointID returns the point of sale product-form stock
// quantities are written against. The design canvas models a single shop
// with one "Главный склад" point and never exposes a per-point breakdown
// on the product form itself (just one "Кол-во (склад)" number per
// variant) — matching that, this picks the first active point of sale
// (falling back to the first point at all if none are marked active), so
// the product form doesn't need its own point selector. ok is false (stock
// upsert skipped, the product/variants themselves still save) only if no
// point of sale exists yet at all — e.g. a fresh install before Wave 4
// Task 4 (Точки) has been used to create one.
func (h *handlers) resolveDefaultPointID(ctx context.Context) (id string, ok bool) {
	pts, err := h.pointsRepo.List(ctx)
	if err != nil || len(pts) == 0 {
		return "", false
	}
	for _, p := range pts {
		if p.IsActive {
			return p.ID, true
		}
	}
	return pts[0].ID, true
}

// saveImages replaces productID's full photo set from the submitted
// image_object_key[] array (sort order = submission order) — object keys
// come from the form's own presigned-upload JS (see product_form.gohtml),
// never typed in directly.
func (h *handlers) saveImages(ctx context.Context, productID string, r *http.Request) error {
	keys := r.Form["image_object_key"]
	inputs := make([]catalog.ImageInput, 0, len(keys))
	for i, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		inputs = append(inputs, catalog.ImageInput{ObjectKey: k, SortOrder: i})
	}
	_, err := h.images.ReplaceForProduct(ctx, productID, inputs)
	return err
}

// --- row actions ---

// productToggleActive handles POST /admin/products/{id}/toggle-active —
// the row menu's "Деактивировать"/"Активировать" action, open to
// owner+manager alike (routes.go). A plain (non-HTMX) <form> post, so it
// works with JS disabled; redirectWithToast still handles the HX-Request
// case defensively.
func (h *handlers) productToggleActive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	product, err := h.products.GetByIDAny(ctx, id)
	if err != nil {
		redirectWithToast(w, r, "/admin/products", appErrMessage(err))
		return
	}

	_, err = h.products.Update(ctx, id, catalog.ProductInput{
		CategoryID:    product.CategoryID,
		NameRu:        product.NameRu,
		NameKy:        product.NameKy,
		DescriptionRu: product.DescriptionRu,
		DescriptionKy: product.DescriptionKy,
		Brand:         product.Brand,
		BasePrice:     product.BasePrice,
		IsActive:      !product.IsActive,
	})
	if err != nil {
		redirectWithToast(w, r, "/admin/products", appErrMessage(err))
		return
	}

	toast := "Товар деактивирован"
	if !product.IsActive {
		toast = "Товар активирован"
	}
	redirectWithToast(w, r, "/admin/products", toast)
}

// productDelete handles POST /admin/products/{id}/delete — the row menu's
// owner-only "Удалить" action (routes.go gates this to ownerOnly, unlike
// toggle-active), triggered via the shared confirm modal
// (openConfirmModal, layout.gohtml) per the task brief, so this always
// arrives as an HTMX request and redirectWithToast responds with
// HX-Redirect.
//
// catalog.ProductRepo has no hard-delete (see its Delete doc comment: a
// past order's order_items still needs to resolve the product it
// references) — so "Удалить" ends up performing the exact same
// is_active=false as "Деактивировать" above. The two actions stay distinct
// in the UI (different RBAC gate, danger styling, confirm prompt) because
// that's what the design calls for, even though today's schema can't make
// deleting a product return that any harder than deactivating.
func (h *handlers) productDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.products.Delete(r.Context(), id); err != nil {
		redirectWithToast(w, r, "/admin/products", appErrMessage(err))
		return
	}
	redirectWithToast(w, r, "/admin/products", "Товар удалён")
}

// --- import screen ---

// productImportPage handles GET /admin/products/import: renders the
// upload form. The form's own JS does the actual work — POSTing the
// chosen file as multipart/form-data straight to the existing
// POST /admin/products/import JSON endpoint (httpapi.
// RegisterAdminImportRoutes, already wired in cmd/server/main.go ahead of
// this task) and rendering the returned {imported, errors[]} inline. See
// this file's package doc comment for why that endpoint isn't called via
// an internal Go function call instead: it already owns this exact
// method+path, so a second net/http.ServeMux registration for it here
// would panic at startup. This is arguably no worse than the "preferred"
// direct-call option the task brief describes — catalog.ImportProducts'
// own request/response shape is JSON-in/JSON-out either way, and this
// keeps the browser's real multipart upload as one hop instead of two.
func (h *handlers) productImportPage(w http.ResponseWriter, r *http.Request) {
	st, _ := staff.FromContext(r.Context())
	data := ImportPageData{ImportURL: "/admin/products/import"}

	pageData := h.productsShellData("product_import", "Импорт товаров", st)
	pageData.ShowBack = true
	pageData.Data = data
	if err := h.render.Render(w, "product_import", pageData); err != nil {
		http.Error(w, "ошибка рендеринга страницы", http.StatusInternalServerError)
	}
}
