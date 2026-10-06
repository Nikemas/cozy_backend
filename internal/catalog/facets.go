package catalog

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"strings"
)

// FacetFilter scopes Facets to the same product set List would return for
// the same category/search — deliberately without size/color/price/in-stock,
// so picking one filter value never hides the other options.
type FacetFilter struct {
	CategoryID  string
	CategoryIDs []string // takes precedence over CategoryID, as in ListFilter
	Query       string
}

// Facets are the filter options available within a scope: every distinct
// size/color of an active product's variants, and the effective price range
// (variant price_override, else base_price) of those products. PriceMin/
// PriceMax are nil when the scope has no products.
type Facets struct {
	Sizes    []string `json:"sizes"`
	Colors   []string `json:"colors"`
	PriceMin *float64 `json:"price_min"`
	PriceMax *float64 `json:"price_max"`
}

// buildFacetConditions reuses List's WHERE builder so facets and the
// product list always agree on which products are in scope.
func buildFacetConditions(f FacetFilter) (string, []any) {
	conditions, args := buildListConditions(ListFilter{
		CategoryID:  f.CategoryID,
		CategoryIDs: f.CategoryIDs,
		Query:       f.Query,
	})
	return "WHERE " + strings.Join(conditions, " AND "), args
}

// Facets returns the sizes, colors and price range offered by the active
// products in f's scope — one pass over their variants: every distinct
// (size, color) with that pair's effective price range. A product with no
// variants contributes one row with NULL size/color and its base_price,
// exactly as minPriceExpr/maxPriceExpr fall back to base_price for it, so
// the min/max over the rows equal the min/max of the per-product ranges.
func (r *ProductRepo) Facets(ctx context.Context, f FacetFilter) (Facets, error) {
	where, args := buildFacetConditions(f)

	q := `
		SELECT pv.size, pv.color,
		       MIN(COALESCE(pv.price_override, products.base_price)),
		       MAX(COALESCE(pv.price_override, products.base_price))
		FROM products
		LEFT JOIN product_variants pv ON pv.product_id = products.id
		` + where + `
		GROUP BY pv.size, pv.color`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Facets{}, err
	}
	defer func() { _ = rows.Close() }()

	sizes, colors := map[string]bool{}, map[string]bool{}
	var out Facets
	for rows.Next() {
		var size, color sql.NullString
		var lo, hi float64
		if err := rows.Scan(&size, &color, &lo, &hi); err != nil {
			return Facets{}, err
		}
		if size.Valid {
			sizes[size.String] = true
		}
		if color.Valid {
			colors[color.String] = true
		}
		if out.PriceMin == nil || lo < *out.PriceMin {
			out.PriceMin = &lo
		}
		if out.PriceMax == nil || hi > *out.PriceMax {
			out.PriceMax = &hi
		}
	}
	if err := rows.Err(); err != nil {
		return Facets{}, err
	}

	out.Sizes, out.Colors = SortedSizes(sizes), sortedNonEmpty(colors)
	return out, nil
}

// SortedSizes orders sizes numerically where possible ("36" < "36,5" <
// "42"), with non-numeric sizes ("M", "XL") after, alphabetically. Empty
// sizes are dropped.
func SortedSizes(set map[string]bool) []string {
	out := sortedNonEmpty(set)
	sort.SliceStable(out, func(i, j int) bool {
		a, aerr := strconv.ParseFloat(strings.ReplaceAll(out[i], ",", "."), 64)
		b, berr := strconv.ParseFloat(strings.ReplaceAll(out[j], ",", "."), 64)
		switch {
		case aerr == nil && berr == nil:
			return a < b
		case aerr == nil:
			return true
		case berr == nil:
			return false
		default:
			return out[i] < out[j]
		}
	})
	return out
}

func sortedNonEmpty(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for s := range set {
		if s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// SubtreeIDs returns id plus the ids of all its descendants in tree (a
// parent category's facets cover its subcategories' products, as on the
// website's category page). An id missing from tree yields just [id].
func SubtreeIDs(tree []*Category, id string) []string {
	var find func(nodes []*Category) *Category
	find = func(nodes []*Category) *Category {
		for _, c := range nodes {
			if c.ID == id {
				return c
			}
			if found := find(c.Children); found != nil {
				return found
			}
		}
		return nil
	}
	root := find(tree)
	if root == nil {
		return []string{id}
	}
	var ids []string
	var walk func(c *Category)
	walk = func(c *Category) {
		ids = append(ids, c.ID)
		for _, child := range c.Children {
			walk(child)
		}
	}
	walk(root)
	return ids
}
