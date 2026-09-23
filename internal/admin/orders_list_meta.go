package admin

import (
	"context"
	"database/sql"
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
