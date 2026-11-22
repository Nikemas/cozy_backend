package reports

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Detail reports (feat/product-code): one catalog category, one brand or
// one product (by its code) over a period. They follow the summary
// reports' rules: an order line counts as sold when its order counts as a
// sale (SaleConditionSQL — not cancelled; an online-card order only once
// paid), revenue is line price × quantity, and the period is orders'
// created_at in [from, to). Lines of cancelled orders are reported
// separately (CancelledCount); an unpaid online order is neither.

// ProductSalesRow is one product of a category/brand detail report.
type ProductSalesRow struct {
	ProductID string
	Code      string
	Name      string // name_ru, like every report key
	Brand     string // "" = no brand
	IsActive  bool

	OrderCount int     // distinct sale orders with this product
	ItemCount  int     // units sold
	Revenue    float64 // Σ price × quantity over sold lines
	// CancelledCount is units in cancelled orders of the period.
	CancelledCount int
}

// VariantSalesRow is one size/color of the product report.
type VariantSalesRow struct {
	VariantID      string
	Size           string
	Color          string
	OrderCount     int
	ItemCount      int
	Revenue        float64
	CancelledCount int
}

// ProductCodeSales is the product report's sales part: every variant
// (sold or not) and the sale lines grouped by the order's point of sale
// (Row keys resolved like Sales' GroupByPoint; UnknownPointKey for
// orders without a point).
type ProductCodeSales struct {
	Variants []VariantSalesRow
	Points   []Row
}

// ProductRef identifies the product a code report is about.
type ProductRef struct {
	ID       string
	Code     string
	Name     string
	Brand    string
	IsActive bool
}

// StockCell is a product's current stock of one variant at one point.
type StockCell struct {
	VariantID string
	PointID   string
	PointName string
	Warehouse bool
	Active    bool
	Qty       int
}

// TotalProductSales sums rows into one row (Name/IDs empty) — the
// report's «Итого». OrderCount is a sum of per-product distinct orders
// (an order with two of the products counts twice), so the pages total
// only units and money.
func TotalProductSales(rows []ProductSalesRow) ProductSalesRow {
	var t ProductSalesRow
	for _, r := range rows {
		t.ItemCount += r.ItemCount
		t.Revenue += r.Revenue
		t.CancelledCount += r.CancelledCount
		t.OrderCount += r.OrderCount
	}
	return t
}

// TotalVariantSales sums variant rows like TotalProductSales.
func TotalVariantSales(rows []VariantSalesRow) VariantSalesRow {
	var t VariantSalesRow
	for _, r := range rows {
		t.ItemCount += r.ItemCount
		t.Revenue += r.Revenue
		t.CancelledCount += r.CancelledCount
		t.OrderCount += r.OrderCount
	}
	return t
}

// lineAggregates are the per-group sold/cancelled aggregates over order
// lines oi of orders o; shared by the product and variant queries.
const lineAggregates = `
	COUNT(DISTINCT oi.order_id) FILTER (WHERE ` + SaleConditionSQL + `) AS order_count,
	COALESCE(SUM(oi.quantity) FILTER (WHERE ` + SaleConditionSQL + `), 0) AS item_count,
	COALESCE(SUM(oi.price * oi.quantity) FILTER (WHERE ` + SaleConditionSQL + `), 0) AS revenue,
	COALESCE(SUM(oi.quantity) FILTER (WHERE o.status = 'cancelled'), 0) AS cancelled_count`

// productSalesQuery lists every product of scope (a CTE over products p
// selecting id) that is active or had order lines in the period, with its
// sales. Sorted by revenue, then units, then name.
func productSalesQuery(scopeCTE string) string {
	return scopeCTE + `,
		lines AS (
			SELECT pv.product_id, ` + lineAggregates + `
			FROM order_items oi
			JOIN orders o ON o.id = oi.order_id
			JOIN product_variants pv ON pv.id = oi.variant_id
			WHERE o.created_at >= $1 AND o.created_at < $2
			  AND pv.product_id IN (SELECT id FROM scope)
			GROUP BY pv.product_id
		)
		SELECT p.id, COALESCE(p.model_code, ''), p.name_ru, COALESCE(TRIM(p.brand), ''), p.is_active,
		       COALESCE(l.order_count, 0), COALESCE(l.item_count, 0), COALESCE(l.revenue, 0), COALESCE(l.cancelled_count, 0)
		FROM products p
		JOIN scope s ON s.id = p.id
		LEFT JOIN lines l ON l.product_id = p.id
		WHERE p.is_active OR l.product_id IS NOT NULL
		ORDER BY COALESCE(l.revenue, 0) DESC, COALESCE(l.item_count, 0) DESC, p.name_ru, p.id`
}

// CategoryProductSales is the category detail report: every product filed
// under categoryID or any of its subcategories (at any depth).
func (r *Repo) CategoryProductSales(ctx context.Context, categoryID string, from, to time.Time) ([]ProductSalesRow, error) {
	q := productSalesQuery(`
		WITH RECURSIVE tree AS (
			SELECT id FROM categories WHERE id = $3
			UNION
			SELECT c.id FROM categories c JOIN tree t ON c.parent_id = t.id
		),
		scope AS (SELECT p.id FROM products p WHERE p.category_id IN (SELECT id FROM tree))`)
	return r.queryProductSales(ctx, q, from, to, categoryID)
}

// BrandProductSales is the brand detail report: every product whose
// trimmed brand equals brand — the same grouping as BrandSales, so "" is
// the "no brand" bucket.
func (r *Repo) BrandProductSales(ctx context.Context, brand string, from, to time.Time) ([]ProductSalesRow, error) {
	q := productSalesQuery(`
		WITH scope AS (SELECT p.id FROM products p WHERE COALESCE(TRIM(p.brand), '') = $3)`)
	return r.queryProductSales(ctx, q, from, to, strings.TrimSpace(brand))
}

func (r *Repo) queryProductSales(ctx context.Context, q string, from, to time.Time, arg string) ([]ProductSalesRow, error) {
	rows, err := r.db.QueryContext(ctx, q, from, to, arg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []ProductSalesRow{}
	for rows.Next() {
		var p ProductSalesRow
		if err := rows.Scan(&p.ProductID, &p.Code, &p.Name, &p.Brand, &p.IsActive,
			&p.OrderCount, &p.ItemCount, &p.Revenue, &p.CancelledCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProductByCode finds a product by its code, ignoring case and
// surrounding spaces. nil (no error) when there is none.
func (r *Repo) ProductByCode(ctx context.Context, code string) (*ProductRef, error) {
	const q = `
		SELECT id, model_code, name_ru, COALESCE(TRIM(brand), ''), is_active
		FROM products WHERE lower(model_code) = lower($1)`
	var p ProductRef
	err := r.db.QueryRowContext(ctx, q, strings.TrimSpace(code)).Scan(&p.ID, &p.Code, &p.Name, &p.Brand, &p.IsActive)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ProductCodeSales is the product report's sales part for productID.
func (r *Repo) ProductCodeSales(ctx context.Context, productID string, from, to time.Time, pointNames map[string]string) (ProductCodeSales, error) {
	variants, err := r.variantSales(ctx, productID, from, to)
	if err != nil {
		return ProductCodeSales{}, err
	}
	points, err := r.productPointSales(ctx, productID, from, to, pointNames)
	if err != nil {
		return ProductCodeSales{}, err
	}
	return ProductCodeSales{Variants: variants, Points: points}, nil
}

func (r *Repo) variantSales(ctx context.Context, productID string, from, to time.Time) ([]VariantSalesRow, error) {
	const q = `
		WITH lines AS (
			SELECT oi.variant_id, ` + lineAggregates + `
			FROM order_items oi
			JOIN orders o ON o.id = oi.order_id
			JOIN product_variants pv ON pv.id = oi.variant_id
			WHERE o.created_at >= $1 AND o.created_at < $2 AND pv.product_id = $3
			GROUP BY oi.variant_id
		)
		SELECT pv.id, pv.size, pv.color,
		       COALESCE(l.order_count, 0), COALESCE(l.item_count, 0), COALESCE(l.revenue, 0), COALESCE(l.cancelled_count, 0)
		FROM product_variants pv
		LEFT JOIN lines l ON l.variant_id = pv.id
		WHERE pv.product_id = $3
		ORDER BY pv.size, pv.color, pv.id`

	rows, err := r.db.QueryContext(ctx, q, from, to, productID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []VariantSalesRow{}
	for rows.Next() {
		var v VariantSalesRow
		if err := rows.Scan(&v.VariantID, &v.Size, &v.Color,
			&v.OrderCount, &v.ItemCount, &v.Revenue, &v.CancelledCount); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// productPointSales groups productID's sold lines by the order's point of
// sale (merged by display name, sorted by key — like Sales).
func (r *Repo) productPointSales(ctx context.Context, productID string, from, to time.Time, pointNames map[string]string) ([]Row, error) {
	const q = `
		SELECT COALESCE(o.point_id::text, '') AS key,
		       COUNT(DISTINCT oi.order_id) AS order_count,
		       COALESCE(SUM(oi.quantity), 0) AS item_count,
		       COALESCE(SUM(oi.price * oi.quantity), 0) AS revenue
		FROM order_items oi
		JOIN orders o ON o.id = oi.order_id
		JOIN product_variants pv ON pv.id = oi.variant_id
		WHERE o.created_at >= $1 AND o.created_at < $2 AND pv.product_id = $3 AND ` + SaleConditionSQL + `
		GROUP BY 1`

	rows, err := r.db.QueryContext(ctx, q, from, to, productID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	byKey := make(map[string]*Row)
	for rows.Next() {
		var row Row
		if err := rows.Scan(&row.Key, &row.OrderCount, &row.ItemCount, &row.Revenue); err != nil {
			return nil, err
		}
		key := pointDisplayKey(row.Key, pointNames)
		acc, ok := byKey[key]
		if !ok {
			acc = &Row{Key: key}
			byKey[key] = acc
		}
		acc.OrderCount += row.OrderCount
		acc.ItemCount += row.ItemCount
		acc.Revenue += row.Revenue
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sortedRows(byKey, func(_ string, a *Row) Row { return *a }), nil
}

// pointDisplayKey maps a point id to its report key (see pointKey).
func pointDisplayKey(pointID string, pointNames map[string]string) string {
	if pointID == "" {
		return UnknownPointKey
	}
	if name, ok := pointNames[pointID]; ok && name != "" {
		return name
	}
	return pointID
}

// ProductStock is productID's current stock: one cell per existing stock
// row (variant × point), points ordered warehouses last, then by name.
// Not cached — stock must be current.
func (r *Repo) ProductStock(ctx context.Context, productID string) ([]StockCell, error) {
	const q = `
		SELECT s.variant_id, ps.id, ps.name, ps.kind = 'warehouse', ps.is_active, s.quantity
		FROM stock s
		JOIN product_variants pv ON pv.id = s.variant_id
		JOIN points_of_sale ps ON ps.id = s.point_id
		WHERE pv.product_id = $1
		ORDER BY ps.kind = 'warehouse', ps.name, ps.id`

	rows, err := r.db.QueryContext(ctx, q, productID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []StockCell{}
	for rows.Next() {
		var c StockCell
		if err := rows.Scan(&c.VariantID, &c.PointID, &c.PointName, &c.Warehouse, &c.Active, &c.Qty); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
