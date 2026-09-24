// Task 2 (Catalog: shop + product) — html/template + HTMX view layer on
// top of the already-implemented internal/catalog domain (Task B, merged
// to main ahead of this task, see tasks/web-todo.md). Nothing here talks
// to the database directly; it only calls catalog.CategoryRepo/
// ProductRepo/VariantRepo/StockRepo and shapes the result for
// shop.gohtml/product.gohtml.
//
// HTMX pattern used throughout (per web-plan §"Что это значит для
// реализации"): every filter/search/pagination control issues a normal
// hx-get to the *same* full-page URL the browser would otherwise
// navigate to, and uses hx-select to pluck out just the fragment that
// changed (#shop-results, #product-detail) from the full response. This
// means h.shop/h.product have exactly one code path for both a plain
// browser request and an HTMX partial-swap request — no server-side
// content-negotiation branching needed.
package web

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/media"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// shopPageSize mirrors catalog.DefaultPageSize — kept as its own constant
// so a future web-specific page size doesn't have to touch the catalog
// package.
const shopPageSize = catalog.DefaultPageSize

// ShopData backs shop.gohtml (screen "shop") — both the initial full-page
// render and every HTMX partial-swap response (see file doc comment) —
// plus the aside's filter form (_aside_filters.gohtml).
type ShopData struct {
	// BasePath is the current category's canonical URL ("/" or
	// "/catalog/:slug") — every filter control's hx-get target is built
	// from this, so switching a filter never loses the active category.
	BasePath string
	// CategoryName is the active category's name ("" on the home page).
	CategoryName string

	Query    string
	Size     string
	Color    string
	InStock  bool
	Sort     string
	PriceMin int // 0 = not set
	PriceMax int // 0 = not set

	// SizeOptions/ColorOptions are every size/color on sale in the
	// current category; SortOptions back the sort <select>.
	SizeOptions  []FilterOption
	ColorOptions []FilterOption
	SortOptions  []FilterOption
	// HasFilters shows the "reset filters" link.
	HasFilters bool

	Page       int
	TotalPages int
	HasPrev    bool
	HasNext    bool
	PrevHref   string
	NextHref   string

	// ShowBanner mirrors §3.1: the promo banner hides once a search,
	// category or filter is active.
	ShowBanner bool

	// Categories backs both the shop's top chip bar and the aside's
	// "Категории" pills — the real top-level category tree.
	Categories []CategoryChip

	Products  []ProductCard
	Total     int
	NoResults bool
}

// FilterOption is one choice in a filter control.
type FilterOption struct {
	Value    string
	Label    string
	Selected bool
}

// CategoryChip is one entry in the category chip bar / aside pill list.
type CategoryChip struct {
	Label  string
	Href   string
	Active bool
}

// ProductCard is one tile in the shop grid.
type ProductCard struct {
	ID        string
	Name      string
	Brand     string
	PriceText string
	// PriceFrom marks a product whose variants differ in price — the card
	// then reads "от <min>".
	PriceFrom bool
	DetailURL string

	// HasPhoto/PhotoURL back the grid thumbnail — false/"" for a product
	// with no row in product_images, in which case the template falls
	// back to the SVG placeholder.
	HasPhoto bool
	PhotoURL string
}

func (h *handlers) shop(w http.ResponseWriter, r *http.Request) error {
	data := h.base(r, "shop")

	shopData, err := h.buildShopData(r, data.Lang)
	if err != nil {
		return err
	}
	data.Data = shopData
	data.SearchQuery = shopData.Query
	h.shopSEO(r, &data, shopData)
	return h.render.Render(w, "shop", data)
}

// shopSEO sets the catalog's title/description/canonical. Filtered and
// sorted views are canonical to the unfiltered category (page kept);
// search results aren't indexed at all.
func (h *handlers) shopSEO(r *http.Request, data *PageData, sd *ShopData) {
	lang := data.Lang
	switch {
	case sd.CategoryName != "":
		data.SEO.Title = fmt.Sprintf(h.t(lang, "seo.category.title"), sd.CategoryName)
		data.SEO.Description = fmt.Sprintf(h.t(lang, "seo.category.description"), sd.CategoryName)
	default:
		data.SEO.Title = h.t(lang, "seo.home.title")
		data.SEO.Description = h.t(lang, "seo.home.description")
		data.SEO.JSONLD = websiteJSONLD(h.siteURL(r))
	}
	canonical := sd.BasePath
	if sd.Page > 1 {
		canonical += "?page=" + strconv.Itoa(sd.Page)
	}
	h.setCanonical(r, data, canonical)
	if sd.Query != "" {
		data.NoIndex = true
	}
}

func (h *handlers) buildShopData(r *http.Request, lang string) (*ShopData, error) {
	ctx := r.Context()
	params := parseShopParams(r.URL.Query())

	tree, err := h.categories.Tree(ctx)
	if err != nil {
		return nil, err
	}

	categorySlug := r.PathValue("slug") // only set on GET /catalog/{slug}
	basePath := "/"
	var categoryIDs []string
	categoryName := ""
	if categorySlug != "" {
		cat, ids := findCategory(tree, categorySlug)
		if cat == nil {
			return nil, apperr.NotFound("category_not_found", "категория не найдена")
		}
		basePath = "/catalog/" + cat.Slug
		categoryIDs = ids
		categoryName = pickName(cat.NameRu, cat.NameKy, lang)
	}

	filter := params.listFilter(categoryIDs, shopPageSize)
	products, total, err := h.products.List(ctx, filter)
	if err != nil {
		return nil, err
	}

	ids := make([]string, len(products))
	for i, p := range products {
		ids[i] = p.ID
	}
	images, err := h.images.PrimaryForProducts(ctx, ids)
	if err != nil {
		return nil, err
	}
	prices, err := h.effectivePrices(ctx, ids)
	if err != nil {
		return nil, err
	}
	facets, err := h.loadFacets(ctx, categoryIDs)
	if err != nil {
		return nil, err
	}

	cards := make([]ProductCard, 0, len(products))
	for _, p := range products {
		price := p.BasePrice
		from := false
		if pr, ok := prices[p.ID]; ok {
			price = pr.Min
			from = pr.Max > pr.Min
		}
		card := ProductCard{
			ID:        p.ID,
			Name:      pickName(p.NameRu, p.NameKy, lang),
			Brand:     stringOr(p.Brand, ""),
			PriceText: formatAmount(price, h.t(lang, "common.currency")),
			PriceFrom: from,
			DetailURL: ProductPath(p.ID, p.NameRu),
		}
		if img, ok := images[p.ID]; ok {
			card.HasPhoto = true
			card.PhotoURL = h.photoURL(media.ThumbKey(img.ObjectKey))
		}
		cards = append(cards, card)
	}

	chips := make([]CategoryChip, 0, len(tree)+1)
	chips = append(chips, CategoryChip{
		Label:  h.bundle.T(lang, "sidebar.category.all"),
		Href:   "/",
		Active: categorySlug == "",
	})
	for _, c := range tree {
		_, subtree := findCategory([]*catalog.Category{c}, categorySlug)
		chips = append(chips, CategoryChip{
			Label:  pickName(c.NameRu, c.NameKy, lang),
			Href:   "/catalog/" + c.Slug,
			Active: categorySlug != "" && len(subtree) > 0,
		})
	}

	totalPages := 1
	if total > 0 {
		totalPages = int(math.Ceil(float64(total) / float64(shopPageSize)))
	}
	page := params.Page
	if page > totalPages {
		page = totalPages
	}

	sd := &ShopData{
		BasePath:     basePath,
		CategoryName: categoryName,
		Query:        params.Query,
		Size:         params.Size,
		Color:        params.Color,
		InStock:      params.InStock,
		Sort:         params.Sort,
		PriceMin:     params.PriceMin,
		PriceMax:     params.PriceMax,
		SizeOptions:  filterOptions(facets.Sizes, params.Size),
		ColorOptions: filterOptions(facets.Colors, params.Color),
		SortOptions: []FilterOption{
			{Value: "", Label: h.bundle.T(lang, "shop.sort.newest"), Selected: params.Sort == ""},
			{Value: catalog.SortPriceAsc, Label: h.bundle.T(lang, "shop.sort.price_asc"), Selected: params.Sort == catalog.SortPriceAsc},
			{Value: catalog.SortPriceDesc, Label: h.bundle.T(lang, "shop.sort.price_desc"), Selected: params.Sort == catalog.SortPriceDesc},
		},
		HasFilters: params.hasFilters(),
		Page:       page,
		TotalPages: totalPages,
		HasPrev:    page > 1,
		HasNext:    page < totalPages,
		ShowBanner: params.Query == "" && categorySlug == "" && !params.hasFilters() && page == 1,
		Categories: chips,
		Products:   cards,
		Total:      total,
		NoResults:  len(cards) == 0,
	}
	sd.PrevHref = params.href(basePath, page-1)
	sd.NextHref = params.href(basePath, page+1)
	return sd, nil
}

// filterOptions marks selected among values; a selected value that's no
// longer on sale is kept so the visitor can still see and clear it.
func filterOptions(values []string, selected string) []FilterOption {
	out := make([]FilterOption, 0, len(values)+1)
	found := false
	for _, v := range values {
		out = append(out, FilterOption{Value: v, Label: v, Selected: v == selected})
		found = found || v == selected
	}
	if selected != "" && !found {
		out = append(out, FilterOption{Value: selected, Label: selected, Selected: true})
	}
	return out
}

// ProductData backs product.gohtml (screen "product") and its
// HTMX-swapped #product-detail fragment (size/color change).
type ProductData struct {
	// DeliveryFee is the courier fee for one pair of this product: the flat
	// DELIVERY_FEE_SOM, or the cheapest active zone (DeliveryFrom when zones
	// differ) — the same numbers the cart and checkout show.
	DeliveryFee  float64
	DeliveryFrom bool
	Name         string
	Brand        string
	Price        float64 // selected variant's price (JSON-LD)
	PriceText    string
	Description  string // in the visitor's language; "" if none

	HasPhoto bool // see ProductCard.HasPhoto
	PhotoURL string
	// Photos is the full gallery for the selected color (first = PhotoURL);
	// thumbnails switch the main photo client-side.
	Photos []ProductPhoto

	// InStock reports whether the selected size/color has stock anywhere;
	// Availability lists the active points of sale that have it.
	InStock      bool
	Availability []PointStock

	Sizes  []SizeOption
	Colors []ColorOption

	SelectedSize      string
	SelectedColor     string
	SelectedVariantID string // "" if the size/color combo doesn't exist

	ProductPath       string
	CartActionURL     string
	BuyActionURL      string
	FavoriteActionURL string
}

// ProductPhoto is one gallery image: the 1200px full variant and its
// 400px thumbnail (both square, see internal/media/normalize.go).
type ProductPhoto struct {
	URL      string
	ThumbURL string
}

// PointStock is one point of sale that has the selected variant in stock.
// Few is set for the last pairs (≤ fewStockThreshold), shown as "осталось N".
type PointStock struct {
	Name    string
	Address string
	Qty     int
	Few     bool
}

// fewStockThreshold is the quantity at or below which a point shows
// "осталось N" instead of a plain "в наличии".
const fewStockThreshold = 2

// buildAvailability lists the points (in branches' order — active points
// only, by name) holding variantID, from the variant's stock rows.
func buildAvailability(variantID string, stock []catalog.StockEntry, branches []storefront.Branch) []PointStock {
	if variantID == "" {
		return nil
	}
	qtyByPoint := map[string]int{}
	for _, s := range stock {
		if s.VariantID == variantID && s.Quantity > 0 {
			qtyByPoint[s.PointID] += s.Quantity
		}
	}
	out := []PointStock{}
	for _, b := range branches {
		if q := qtyByPoint[b.ID]; q > 0 {
			out = append(out, PointStock{Name: b.Name, Address: b.Address, Qty: q, Few: q <= fewStockThreshold})
		}
	}
	return out
}

// buildGallery turns the selected color's images into gallery entries.
func buildGallery(images []catalog.ProductImage, url func(string) string) []ProductPhoto {
	out := make([]ProductPhoto, 0, len(images))
	for _, img := range images {
		out = append(out, ProductPhoto{URL: url(img.ObjectKey), ThumbURL: url(media.ThumbKey(img.ObjectKey))})
	}
	return out
}

// SizeOption/ColorOption back the size/color picker buttons. Href always
// points at the *current* product with this option selected (and the
// other axis unchanged) — clicking it re-fetches #product-detail via
// HTMX (see product.gohtml).
type SizeOption struct {
	Label     string
	Href      string
	Selected  bool
	Available bool
}

type ColorOption struct {
	Label     string
	Href      string
	Selected  bool
	Available bool
}

func (h *handlers) product(w http.ResponseWriter, r *http.Request) error {
	data := h.base(r, "product")

	productID, ok := ResolveProductID(r.PathValue("slug"))
	if !ok {
		return apperr.NotFound("product_not_found", "товар не найден")
	}

	pd, err := h.buildProductData(r.Context(), r.URL.Query(), data.Lang, productID)
	if err != nil {
		return err
	}

	// One URL per product: an outdated/mistyped slug suffix (e.g. after a
	// rename) or an upper-case id 301s to the canonical path, query kept.
	if target, ok := canonicalRedirect(r, pd.ProductPath); ok {
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return nil
	}

	if err := h.applyProductDelivery(r.Context(), pd); err != nil {
		return err
	}
	data.Data = pd
	h.productSEO(r, &data, pd)
	return h.render.Render(w, "product", data)
}

// canonicalRedirect reports where to 301 a request whose path isn't
// canonicalPath (query preserved). HTMX partial requests are never
// redirected — they always use the canonical path already.
func canonicalRedirect(r *http.Request, canonicalPath string) (string, bool) {
	if r.URL.Path == canonicalPath || isHX(r) {
		return "", false
	}
	if r.URL.RawQuery != "" {
		return canonicalPath + "?" + r.URL.RawQuery, true
	}
	return canonicalPath, true
}

// productSEO sets the product page's title/description, canonical
// (without the size/color query), OpenGraph image and JSON-LD Product.
func (h *handlers) productSEO(r *http.Request, data *PageData, pd *ProductData) {
	lang := data.Lang
	name := pd.Name
	if pd.Brand != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(pd.Brand)) {
		name = pd.Brand + " " + name
	}
	data.SEO.Title = fmt.Sprintf(h.t(lang, "seo.product.title"), name)
	if pd.Description != "" {
		data.SEO.Description = truncateText(pd.Description, 160)
	} else {
		data.SEO.Description = fmt.Sprintf(h.t(lang, "seo.product.description"), name, pd.PriceText)
	}
	h.setCanonical(r, data, pd.ProductPath)
	data.SEO.OGType = "product"
	if pd.PhotoURL != "" {
		data.SEO.OGImage = pd.PhotoURL
	}
	data.SEO.JSONLD = productJSONLD(pd, data.SEO.Canonical, pd.Price)
}

func (h *handlers) buildProductData(ctx context.Context, q url.Values, lang, productID string) (*ProductData, error) {
	product, err := h.products.GetByID(ctx, productID)
	if err != nil {
		return nil, err
	}

	variantList, err := h.variants.ListByProduct(ctx, product.ID)
	if err != nil {
		return nil, err
	}

	variantIDs := make([]string, len(variantList))
	for i, v := range variantList {
		variantIDs[i] = v.ID
	}
	stockRows, err := h.stock.ByVariantIDs(ctx, variantIDs)
	if err != nil {
		return nil, err
	}
	qtyByVariant := make(map[string]int, len(variantList))
	for _, s := range stockRows {
		qtyByVariant[s.VariantID] += s.Quantity
	}

	sizeVals := make([]string, len(variantList))
	colorVals := make([]string, len(variantList))
	for i, v := range variantList {
		sizeVals[i] = v.Size
		colorVals[i] = v.Color
	}
	sizes := distinctInOrder(sizeVals)
	colors := distinctInOrder(colorVals)

	selectedSize := q.Get("size")
	selectedColor := q.Get("color")
	if selectedSize == "" && len(sizes) > 0 {
		selectedSize = sizes[0]
	}
	if selectedColor == "" && len(colors) > 0 {
		selectedColor = colors[0]
	}

	name := pickName(product.NameRu, product.NameKy, lang)
	productPath := ProductPath(product.ID, product.NameRu)

	sizeOpts := make([]SizeOption, 0, len(sizes))
	for _, s := range sizes {
		v := findVariant(variantList, s, selectedColor)
		sizeOpts = append(sizeOpts, SizeOption{
			Label:     s,
			Selected:  s == selectedSize,
			Available: v != nil && qtyByVariant[v.ID] > 0,
			Href:      productPath + "?size=" + url.QueryEscape(s) + "&color=" + url.QueryEscape(selectedColor),
		})
	}
	colorOpts := make([]ColorOption, 0, len(colors))
	for _, c := range colors {
		v := findVariant(variantList, selectedSize, c)
		colorOpts = append(colorOpts, ColorOption{
			Label:     c,
			Selected:  c == selectedColor,
			Available: v != nil && qtyByVariant[v.ID] > 0,
			Href:      productPath + "?size=" + url.QueryEscape(selectedSize) + "&color=" + url.QueryEscape(c),
		})
	}

	price := product.BasePrice
	selectedVariantID := ""
	if v := findVariant(variantList, selectedSize, selectedColor); v != nil {
		selectedVariantID = v.ID
		if v.PriceOverride != nil {
			price = *v.PriceOverride
		}
	}

	productImages, err := h.images.ListByProduct(ctx, product.ID)
	if err != nil {
		return nil, err
	}
	photos := buildGallery(catalog.ForColor(productImages, selectedColor), h.photoURL)
	photoURL := ""
	if len(photos) > 0 {
		photoURL = photos[0].URL
	}

	branches, err := h.branchRepo.List(ctx)
	if err != nil {
		return nil, err
	}
	availability := buildAvailability(selectedVariantID, stockRows, branches)

	return &ProductData{
		Name:              name,
		Brand:             stringOr(product.Brand, ""),
		Price:             price,
		PriceText:         formatAmount(price, h.t(lang, "common.currency")),
		Description:       pickName(stringOr(product.DescriptionRu, ""), stringOr(product.DescriptionKy, ""), lang),
		HasPhoto:          len(photos) > 0,
		PhotoURL:          photoURL,
		Photos:            photos,
		InStock:           len(availability) > 0,
		Availability:      availability,
		Sizes:             sizeOpts,
		Colors:            colorOpts,
		SelectedSize:      selectedSize,
		SelectedColor:     selectedColor,
		SelectedVariantID: selectedVariantID,
		ProductPath:       productPath,
		CartActionURL:     productPath + "/cart",
		BuyActionURL:      productPath + "/buy",
		FavoriteActionURL: "/favorites/" + product.ID,
	}, nil
}

func findVariant(variants []catalog.Variant, size, color string) *catalog.Variant {
	for i := range variants {
		if variants[i].Size == size && variants[i].Color == color {
			return &variants[i]
		}
	}
	return nil
}

func distinctInOrder(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// pickName returns nameKy for i18n.LangKY, nameRu otherwise. Product/
// category names are fully bilingual in the schema (name_ru/name_ky are
// both NOT NULL), unlike locales/*.yaml's layout strings.
func pickName(nameRu, nameKy, lang string) string {
	if lang == "ky" && nameKy != "" {
		return nameKy
	}
	return nameRu
}

func stringOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

// photoURL builds a direct (non-presigned) URL to objectKey in the
// cozy-media bucket. The bucket is set to public-read at setup time —
// one of the two options Task E's own notes call out ("presigned GET URL
// (или публичная политика бакета за Caddy/nginx кэшем — решить по
// месту)") — so the storefront doesn't need a media.Client dependency
// just to render <img> tags. The URL is built on cfg.MinIOPublicEndpoint
// (the browser-facing host, e.g. media.cozy.erpsystemsales.com behind
// Caddy), see config.PublicObjectURL.
//
// objectKey is served as given: grid cards pass media.ThumbKey(key) for
// the 400px variant, the product page passes the stored (full) key.
func (h *handlers) photoURL(objectKey string) string {
	return h.cfg.PublicObjectURL(objectKey)
}

// applyProductDelivery fills pd.DeliveryFee / DeliveryFrom via the cart's
// zone logic, treating a single pair at pd.Price as the cart.
func (h *handlers) applyProductDelivery(ctx context.Context, pd *ProductData) error {
	page := &CartPageData{Lines: []CartLineView{{}}, ItemsTotal: pd.Price, DeliveryFee: orders.CurrentSettings().DeliveryFee}
	if h.zones != nil {
		zones, err := h.zones.ListActive(ctx)
		if err != nil {
			return err
		}
		applyZoneDelivery(page, zones)
	}
	pd.DeliveryFee, pd.DeliveryFrom = page.DeliveryFee, page.DeliveryFrom
	return nil
}
