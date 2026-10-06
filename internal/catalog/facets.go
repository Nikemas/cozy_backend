package catalog

import (
	"context"
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
// products in f's scope.
func (r *ProductRepo) Facets(ctx context.Context, f FacetFilter) (Facets, error) {
	where, args := buildFacetConditions(f)

	variantQuery := `
		SELECT DISTINCT pv.size, pv.color
		FROM product_variants pv
		WHERE pv.product_id IN (SELECT id FROM products ` + where + `)`
	rows, err := r.db.QueryContext(ctx, variantQuery, args...)
	if err != nil {
		return Facets{}, err
	}
	defer func() { _ = rows.Close() }()

	sizes, colors := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var size, color string
		if err := rows.Scan(&size, &color); err != nil {
			return Facets{}, err
		}
		sizes[size] = true
		colors[color] = true
	}
	if err := rows.Err(); err != nil {
		return Facets{}, err
	}

	out := Facets{Sizes: SortedSizes(sizes), Colors: sortedNonEmpty(colors)}
	priceQuery := `SELECT MIN(` + minPriceExpr + `), MAX(` + maxPriceExpr + `) FROM products ` + where
	if err := r.db.QueryRowContext(ctx, priceQuery, args...).Scan(&out.PriceMin, &out.PriceMax); err != nil {
		return Facets{}, err
	}
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
