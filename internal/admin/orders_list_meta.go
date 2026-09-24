package admin

import (
	"context"
	"database/sql"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

// orderListMeta batch-loads the per-row extras the order list shows
// (line-item count, customer phone) for a whole page at once. It replaced
// a per-row orders.Service.AdminGetOrder + CustomerRepo.GetByID loop — up
// to ~2×AdminPageSize extra queries per page view — with exactly two
// queries. Lives here, not in internal/orders, because it is a read-model
// for this one screen.
type orderListMeta interface {
	// ItemCounts returns order_id -> number of order_items rows; orders
	// with no items are absent (read as 0).
	ItemCounts(ctx context.Context, orderIDs []string) (map[string]int, error)
	// CustomerPhones returns customer_id -> phone.
	CustomerPhones(ctx context.Context, customerIDs []string) (map[string]string, error)
	// Search is the list page's filtered query (orders_search.go).
	Search(ctx context.Context, f orderSearchFilter) ([]orders.Order, int, error)
	// VariantThumbs returns variant_id -> object key of the photo that
	// best shows that variant; variants of products with no photo are
	// absent.
	VariantThumbs(ctx context.Context, variantIDs []string) (map[string]string, error)
	// OrderThumbs returns order_id -> the photo object key of the order's
	// first line item (by line id), for the list's thumbnail.
	OrderThumbs(ctx context.Context, orderIDs []string) (map[string]string, error)
}

type orderListMetaRepo struct {
	db *sql.DB
}

func newOrderListMetaRepo(db *sql.DB) *orderListMetaRepo {
	return &orderListMetaRepo{db: db}
}

func (r *orderListMetaRepo) ItemCounts(ctx context.Context, orderIDs []string) (map[string]int, error) {
	out := make(map[string]int, len(orderIDs))
	if len(orderIDs) == 0 {
		return out, nil
	}
	const q = `
		SELECT order_id, COUNT(*)
		FROM order_items
		WHERE order_id = ANY($1)
		GROUP BY order_id`

	rows, err := r.db.QueryContext(ctx, q, orderIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (r *orderListMetaRepo) CustomerPhones(ctx context.Context, customerIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(customerIDs))
	if len(customerIDs) == 0 {
		return out, nil
	}
	const q = `SELECT id, phone FROM customers WHERE id = ANY($1)`

	rows, err := r.db.QueryContext(ctx, q, customerIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, phone string
		if err := rows.Scan(&id, &phone); err != nil {
			return nil, err
		}
		out[id] = phone
	}
	return out, rows.Err()
}

// thumbRankSQL orders a product's photos for one variant: a photo tagged
// with the variant's own color first, then general (untagged) photos,
// then other colors — each group by sort_order. Same preference as
// catalog.ForColor on the storefront.
const thumbRankSQL = `CASE WHEN pi.color = pv.color THEN 0 WHEN pi.color IS NULL THEN 1 ELSE 2 END, pi.sort_order`

func (r *orderListMetaRepo) VariantThumbs(ctx context.Context, variantIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(variantIDs))
	if len(variantIDs) == 0 {
		return out, nil
	}
	q := `
		SELECT DISTINCT ON (pv.id) pv.id, pi.object_key
		FROM product_variants pv
		JOIN product_images pi ON pi.product_id = pv.product_id
		WHERE pv.id = ANY($1)
		ORDER BY pv.id, ` + thumbRankSQL
	return r.keyValues(ctx, q, variantIDs, out)
}

func (r *orderListMetaRepo) OrderThumbs(ctx context.Context, orderIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(orderIDs))
	if len(orderIDs) == 0 {
		return out, nil
	}
	q := `
		SELECT DISTINCT ON (oi.order_id) oi.order_id, pi.object_key
		FROM order_items oi
		JOIN product_variants pv ON pv.id = oi.variant_id
		JOIN product_images pi ON pi.product_id = pv.product_id
		WHERE oi.order_id = ANY($1)
		ORDER BY oi.order_id, oi.id, ` + thumbRankSQL
	return r.keyValues(ctx, q, orderIDs, out)
}

func (r *orderListMetaRepo) keyValues(ctx context.Context, q string, ids []string, out map[string]string) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
