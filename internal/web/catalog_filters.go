package web

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

// shopParams are the shop/catalog screen's query-string filters. Parsed
// once per request and re-encoded for every link that must keep them
// (pagination, category chips' no-JS hrefs).
type shopParams struct {
	Query    string
	Size     string
	Color    string
	InStock  bool
	Sort     string // "" (newest), catalog.SortPriceAsc, catalog.SortPriceDesc, catalog.SortPopular
	PriceMin int    // 0 = no lower bound
	PriceMax int    // 0 = no upper bound
	Page     int    // >= 1
}

// maxQueryLen caps the search string so a pasted wall of text doesn't
// turn into a huge ILIKE pattern.
const maxQueryLen = 100

func parseShopParams(q url.Values) shopParams {
	p := shopParams{
		Query: strings.TrimSpace(q.Get("q")),
		Size:  strings.TrimSpace(q.Get("size")),
		Color: strings.TrimSpace(q.Get("color")),
		Page:  1,
	}
	if r := []rune(p.Query); len(r) > maxQueryLen {
		p.Query = string(r[:maxQueryLen])
	}
	switch v := q.Get("in_stock"); v {
	case "1", "true", "on":
		p.InStock = true
	}
	switch v := q.Get("sort"); v {
	case catalog.SortPriceAsc, catalog.SortPriceDesc, catalog.SortPopular:
		p.Sort = v
	}
	p.PriceMin = positiveInt(q.Get("price_min"))
	p.PriceMax = positiveInt(q.Get("price_max"))
	if p.PriceMin > 0 && p.PriceMax > 0 && p.PriceMin > p.PriceMax {
		p.PriceMin, p.PriceMax = p.PriceMax, p.PriceMin
	}
	if n := positiveInt(q.Get("page")); n > 0 {
		p.Page = n
	}
	return p
}

func positiveInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// hasFilters reports whether any filter beyond search/sort is active.
func (p shopParams) hasFilters() bool {
	return p.Size != "" || p.Color != "" || p.InStock || p.PriceMin > 0 || p.PriceMax > 0
}

// values encodes p for a link to page (page 1 is left implicit).
func (p shopParams) values(page int) url.Values {
	v := url.Values{}
	if p.Query != "" {
		v.Set("q", p.Query)
	}
	if p.Size != "" {
		v.Set("size", p.Size)
	}
	if p.Color != "" {
		v.Set("color", p.Color)
	}
	if p.InStock {
		v.Set("in_stock", "1")
	}
	if p.Sort != "" {
		v.Set("sort", p.Sort)
	}
	if p.PriceMin > 0 {
		v.Set("price_min", strconv.Itoa(p.PriceMin))
	}
	if p.PriceMax > 0 {
		v.Set("price_max", strconv.Itoa(p.PriceMax))
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	return v
}

// href builds basePath + p's filters for page.
func (p shopParams) href(basePath string, page int) string {
	if enc := p.values(page).Encode(); enc != "" {
		return basePath + "?" + enc
	}
	return basePath
}

// listFilter maps p onto the catalog query.
func (p shopParams) listFilter(categoryIDs []string, pageSize int) catalog.ListFilter {
	f := catalog.ListFilter{
		CategoryIDs: categoryIDs,
		Query:       p.Query,
		Size:        p.Size,
		Color:       p.Color,
		InStock:     p.InStock,
		Sort:        p.Sort,
		Page:        p.Page,
		PageSize:    pageSize,
	}
	if f.Sort == "" {
		f.Sort = catalog.SortNewest
	}
	if p.PriceMin > 0 {
		v := float64(p.PriceMin)
		f.PriceMin = &v
	}
	if p.PriceMax > 0 {
		v := float64(p.PriceMax)
		f.PriceMax = &v
	}
	return f
}

// findCategory returns the category with slug in tree plus the ids of it
// and all its descendants (a parent category's page lists its
// subcategories' products too).
func findCategory(tree []*catalog.Category, slug string) (*catalog.Category, []string) {
	for _, c := range tree {
		if c.Slug == slug {
			var ids []string
			walkCategories([]*catalog.Category{c}, func(n *catalog.Category) { ids = append(ids, n.ID) })
			return c, ids
		}
		if found, ids := findCategory(c.Children, slug); found != nil {
			return found, ids
		}
	}
	return nil, nil
}

// priceRange is a product's effective price range across its variants.
type priceRange struct{ Min, Max float64 }

// effectivePrices returns each product's cheapest/dearest variant price
// (price_override, else base_price) — what the grid shows, so a card never
// advertises a base_price no variant actually sells at.
func (h *handlers) effectivePrices(ctx context.Context, productIDs []string) (map[string]priceRange, error) {
	out := make(map[string]priceRange, len(productIDs))
	if len(productIDs) == 0 {
		return out, nil
	}
	const q = `
		SELECT p.id,
		       COALESCE(MIN(COALESCE(pv.price_override, p.base_price)), p.base_price),
		       COALESCE(MAX(COALESCE(pv.price_override, p.base_price)), p.base_price)
		FROM products p
		LEFT JOIN product_variants pv ON pv.product_id = p.id
		WHERE p.id = ANY($1)
		GROUP BY p.id, p.base_price`
	rows, err := h.db.QueryContext(ctx, q, productIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var pr priceRange
		if err := rows.Scan(&id, &pr.Min, &pr.Max); err != nil {
			return nil, err
		}
		out[id] = pr
	}
	return out, rows.Err()
}

// loadFacets returns the filter options offered for the current category:
// every size/color of an active product's variant (catalog.ProductRepo.Facets,
// shared with GET /api/v1/products/facets).
func (h *handlers) loadFacets(ctx context.Context, categoryIDs []string) (catalog.Facets, error) {
	return h.products.Facets(ctx, catalog.FacetFilter{CategoryIDs: categoryIDs})
}
