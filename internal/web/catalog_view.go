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
	"math"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
)

// shopPageSize mirrors catalog.DefaultPageSize — kept as its own constant
// so a future web-specific page size doesn't have to touch the catalog
// package.
const shopPageSize = catalog.DefaultPageSize

// priceSliderMin/Max mirror the design's price-filter range
// (design-system/COZY_WEB_DESIGN.md §4 editable props: priceMax default
// range 1000–6000) — a fixed placeholder until the catalog has enough
// products to compute a real max(base_price) per category.
const (
	priceSliderMin = 1000
	priceSliderMax = 6000
)

// ShopData backs shop.gohtml (screen "shop") — both the initial full-page
// render and every HTMX partial-swap response (see file doc comment).
type ShopData struct {
	// BasePath is the current category's canonical URL ("/" or
	// "/catalog/:slug") — every filter control's hx-get target is built
	// from this, so switching a filter never loses the active category.
	BasePath string

	Query    string
	PriceMax int // 0 = no price filter applied

	// PriceSliderMin/Max are the range input's fixed bounds — see
	// priceSliderMin/Max's doc comment for why they're a placeholder.
	PriceSliderMin int
	PriceSliderMax int

	Page       int
	TotalPages int
	HasPrev    bool
	HasNext    bool
	PrevHref   string
	NextHref   string

	// ShowBanner mirrors §3.1: the promo banner hides once a search or
	// category filter is active.
	ShowBanner bool

	// Categories backs both the shop's top chip bar and the aside's
	// "Категории" pills — the real category tree (catalog.CategoryRepo.
	// Tree), not the hardcoded gender pills the prototype/Foundation
	// placeholder used, since the catalog schema has no gender concept.
	Categories []CategoryChip

	Products  []ProductCard
	Total     int
	NoResults bool
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
	return h.render.Render(w, "shop", data)
}

func (h *handlers) buildShopData(r *http.Request, lang string) (*ShopData, error) {
	ctx := r.Context()
	q := r.URL.Query()

	categorySlug := r.PathValue("slug") // only set on GET /catalog/{slug}
	basePath := "/"
	if categorySlug != "" {
		basePath = "/catalog/" + categorySlug
	}

	filter := catalog.ListFilter{
		Query:    q.Get("q"),
		Sort:     catalog.SortNewest,
		Page:     1,
		PageSize: shopPageSize,
	}
	if v := q.Get("page"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			filter.Page = p
		}
	}

	priceMax := 0
	if v := q.Get("price_max"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			priceMax = p
			pf := float64(p)
			filter.PriceMax = &pf
		}
	}

	if categorySlug != "" {
		id, err := h.categories.ResolveID(ctx, categorySlug)
		if err != nil {
			return nil, err
		}
		filter.CategoryID = id
	}

	tree, err := h.categories.Tree(ctx)
	if err != nil {
		return nil, err
	}

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

	cards := make([]ProductCard, 0, len(products))
	for _, p := range products {
		name := pickName(p.NameRu, p.NameKy, lang)
		card := ProductCard{
			ID:        p.ID,
			Name:      name,
			Brand:     stringOr(p.Brand, ""),
			PriceText: formatMoney(p.BasePrice),
			DetailURL: ProductPath(p.ID, p.NameRu),
		}
		if img, ok := images[p.ID]; ok {
			card.HasPhoto = true
			card.PhotoURL = h.photoURL(img.ObjectKey)
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
		chips = append(chips, CategoryChip{
			Label:  pickName(c.NameRu, c.NameKy, lang),
			Href:   "/catalog/" + c.Slug,
			Active: c.Slug == categorySlug,
		})
	}

	totalPages := 1
	if total > 0 {
		totalPages = int(math.Ceil(float64(total) / float64(filter.PageSize)))
	}
	if filter.Page > totalPages {
		filter.Page = totalPages
	}

	sd := &ShopData{
		BasePath:       basePath,
		Query:          filter.Query,
		PriceMax:       priceMax,
		PriceSliderMin: priceSliderMin,
		PriceSliderMax: priceSliderMax,
		Page:           filter.Page,
		TotalPages:     totalPages,
		HasPrev:        filter.Page > 1,
		HasNext:        filter.Page < totalPages,
		ShowBanner:     filter.Query == "" && categorySlug == "",
		Categories:     chips,
		Products:       cards,
		Total:          total,
		NoResults:      len(cards) == 0,
	}
	sd.PrevHref = shopPageHref(basePath, filter, filter.Page-1)
	sd.NextHref = shopPageHref(basePath, filter, filter.Page+1)
	return sd, nil
}

// shopPageHref builds a shop/catalog URL for page, preserving the
// current search/price filters (but never `category`, which is already
// baked into basePath).
func shopPageHref(basePath string, filter catalog.ListFilter, page int) string {
	v := url.Values{}
	if filter.Query != "" {
		v.Set("q", filter.Query)
	}
	if filter.PriceMax != nil {
		v.Set("price_max", strconv.Itoa(int(*filter.PriceMax)))
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if enc := v.Encode(); enc != "" {
		return basePath + "?" + enc
	}
	return basePath
}

// ProductData backs product.gohtml (screen "product") and its
// HTMX-swapped #product-detail fragment (size/color change).
type ProductData struct {
	Name      string
	Brand     string
	PriceText string

	HasPhoto bool // see ProductCard.HasPhoto
	PhotoURL string

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
	data.Data = pd
	return h.render.Render(w, "product", data)
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

	hasPhoto := false
	photoURL := ""
	if images, err := h.images.PrimaryForProducts(ctx, []string{product.ID}); err != nil {
		return nil, err
	} else if img, ok := images[product.ID]; ok {
		hasPhoto = true
		photoURL = h.photoURL(img.ObjectKey)
	}

	return &ProductData{
		Name:              name,
		Brand:             stringOr(product.Brand, ""),
		PriceText:         formatMoney(price),
		HasPhoto:          hasPhoto,
		PhotoURL:          photoURL,
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

// formatMoney renders a KGS amount the way the design's prototype does —
// thousands grouped with a space, " сом" suffix, no decimals (mirrors
// design/Shoebox Web's `money(n)` helper: n.toLocaleString('ru-RU') +
// ' сом'). Prices are stored as NUMERIC(10,2) but soms aren't split into
// cents in practice, so this rounds to the nearest whole som.
func formatMoney(v float64) string {
	n := int64(math.Round(v))
	neg := n < 0
	if neg {
		n = -n
	}
	digits := strconv.FormatInt(n, 10)

	var grouped []byte
	for i := 0; i < len(digits); i++ {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped = append(grouped, ' ')
		}
		grouped = append(grouped, digits[i])
	}
	out := string(grouped)
	if neg {
		out = "-" + out
	}
	return out + " сом"
}

// photoURL builds a direct (non-presigned) URL to objectKey in the
// cozy-media bucket. The bucket is set to public-read at setup time —
// one of the two options Task E's own notes call out ("presigned GET URL
// (или публичная политика бакета за Caddy/nginx кэшем — решить по
// месту)") — so the storefront doesn't need a media.Client dependency
// just to render <img> tags. The URL is built on cfg.MinIOPublicEndpoint
// (the browser-facing host, e.g. media.cozy.erpsystemsales.com behind
// Caddy), see config.PublicObjectURL.
func (h *handlers) photoURL(objectKey string) string {
	return h.cfg.PublicObjectURL(objectKey)
}
