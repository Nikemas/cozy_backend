package reports

import (
	"context"
	"database/sql"
	"time"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

// Repo loads the raw data AggregateSales needs straight from Postgres. It
// deliberately doesn't reuse internal/orders.Service: that type's queries
// are all scoped to one customer_id (ListOrders/GetOrder), while this
// report is admin-wide across every customer — a different WHERE clause,
// not a variation the customer-scoped Service exposes. The column list and
// batch-items pattern below mirror orders.(*Service).ListOrders/attachItems
// exactly, so the same SQL shape carries the same behavior.
type Repo struct {
	db *sql.DB
}

func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// LoadOrders returns every order (with its items attached) created in
// [from, to) — from inclusive, to exclusive. Callers building a report over
// whole calendar days (e.g. "2026-01-01".."2026-01-31") should pass to as
// the day *after* the last day they want included.
func (r *Repo) LoadOrders(ctx context.Context, from, to time.Time) ([]orders.Order, error) {
	const q = `
		SELECT id, order_number, customer_id, address_id, point_id, status, payment_method, payment_status, total_amount, comment, created_at, updated_at
		FROM orders
		WHERE created_at >= $1 AND created_at < $2
		ORDER BY created_at ASC`

	rows, err := r.db.QueryContext(ctx, q, from, to)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	list := []orders.Order{}
	for rows.Next() {
		var o orders.Order
		if err := rows.Scan(&o.ID, &o.OrderNumber, &o.CustomerID, &o.AddressID, &o.PointID,
			&o.Status, &o.PaymentMethod, &o.PaymentStatus, &o.TotalAmount, &o.Comment, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := r.attachItems(ctx, list); err != nil {
		return nil, err
	}
	return list, nil
}

// attachItems batch-loads order_items for every order in list with one
// = ANY($1) array parameter — a fixed statement, unlike the former
// IN ($1..$N) list, which built a new statement per order count and hit
// Postgres's 65535-parameter limit on a large range.
func (r *Repo) attachItems(ctx context.Context, list []orders.Order) error {
	if len(list) == 0 {
		return nil
	}

	ids := make([]string, len(list))
	idxByID := make(map[string]int, len(list))
	for i, o := range list {
		ids[i] = o.ID
		idxByID[o.ID] = i
	}

	const q = `
		SELECT id, order_id, variant_id, product_name_snapshot, size_snapshot, color_snapshot, quantity, price
		FROM order_items
		WHERE order_id = ANY($1)
		ORDER BY id`

	rows, err := r.db.QueryContext(ctx, q, ids)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var it orders.OrderItem
		if err := rows.Scan(&it.ID, &it.OrderID, &it.VariantID, &it.ProductNameSnapshot,
			&it.SizeSnapshot, &it.ColorSnapshot, &it.Quantity, &it.Price); err != nil {
			return err
		}
		i, ok := idxByID[it.OrderID]
		if !ok {
			continue
		}
		list[i].Items = append(list[i].Items, it)
	}
	return rows.Err()
}

// PointNames returns a point_id -> name map for every point of sale, for
// resolving GroupByPoint's keys to something readable. Reads directly from
// points_of_sale (present since migration 000003) rather than depending on
// whatever admin points-of-sale package Task G/I add — that work is
// in-flight concurrently in this codebase and this report has no other
// reason to depend on it.
func (r *Repo) PointNames(ctx context.Context) (map[string]string, error) {
	const q = `SELECT id, name FROM points_of_sale`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	names := make(map[string]string)
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

// CategorySales aggregates sold line items by top-level catalog category
// over [from, to): a product filed under a subcategory counts toward its
// parent. Only orders that count as sales (SaleConditionSQL) are included;
// revenue is line price × quantity, like AggregateSales' GroupByProduct.
// Rows are sorted by revenue, highest first (ties by name in the database
// collation, as before; the grouping itself uses COLLATE "C" — same
// groups, byte comparisons, see Sales).
func (r *Repo) CategorySales(ctx context.Context, from, to time.Time) ([]Row, error) {
	const q = `
		SELECT category, order_count, item_count, revenue FROM (
			SELECT COALESCE(parent.name_ru, c.name_ru) COLLATE "C" AS category,
			       COUNT(DISTINCT oi.order_id) AS order_count,
			       COALESCE(SUM(oi.quantity), 0) AS item_count,
			       COALESCE(SUM(oi.price * oi.quantity), 0) AS revenue
			FROM order_items oi
			JOIN orders o ON o.id = oi.order_id
			JOIN product_variants pv ON pv.id = oi.variant_id
			JOIN products p ON p.id = pv.product_id
			JOIN categories c ON c.id = p.category_id
			LEFT JOIN categories parent ON parent.id = c.parent_id
			WHERE o.created_at >= $1 AND o.created_at < $2 AND ` + SaleConditionSQL + `
			GROUP BY 1
		) sales
		ORDER BY revenue DESC, category COLLATE "default"`

	return r.queryRows(ctx, q, from, to)
}

// BrandSales aggregates sold line items by product brand over [from, to)
// — the admin report's "По брендам" list. orders.OrderItem snapshots only
// a product name, size and color at checkout, not the brand, so this joins
// order_items.variant_id -> product_variants -> products.brand. Revenue is
// line price × quantity, like AggregateSales' GroupByProduct, so brand rows
// sum to the same total as the product rows. Only orders that count as
// sales (SaleConditionSQL) are included. Products with no brand (or an
// all-whitespace one) roll up into one "" bucket; the admin page labels it
// in the page language. Grouped under COLLATE "C" (same groups, byte
// comparisons — see Sales); rows sorted by revenue, highest first.
func (r *Repo) BrandSales(ctx context.Context, from, to time.Time) ([]Row, error) {
	const q = `
		SELECT COALESCE(TRIM(p.brand), '') COLLATE "C" AS brand,
		       COUNT(DISTINCT oi.order_id) AS order_count,
		       COALESCE(SUM(oi.quantity), 0) AS item_count,
		       COALESCE(SUM(oi.price * oi.quantity), 0) AS revenue
		FROM order_items oi
		JOIN orders o ON o.id = oi.order_id
		JOIN product_variants pv ON pv.id = oi.variant_id
		JOIN products p ON p.id = pv.product_id
		WHERE o.created_at >= $1 AND o.created_at < $2 AND ` + SaleConditionSQL + `
		GROUP BY 1
		ORDER BY revenue DESC`

	return r.queryRows(ctx, q, from, to)
}

// queryRows scans (key, order_count, item_count, revenue) rows in the
// order the query returns them.
func (r *Repo) queryRows(ctx context.Context, q string, from, to time.Time) ([]Row, error) {
	rows, err := r.db.QueryContext(ctx, q, from, to)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Row
	for rows.Next() {
		var row Row
		if err := rows.Scan(&row.Key, &row.OrderCount, &row.ItemCount, &row.Revenue); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
