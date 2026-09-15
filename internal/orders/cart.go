// Package orders holds the cart and order lifecycle: cart_items, orders,
// order_items. Task 1 (Foundation) froze the types and method signatures
// below so internal/web (Tasks 2, 4, 5) could be built against a stable
// contract while Task 3 implemented the SQL bodies (this file/order.go).
package orders

import (
	"context"
	"database/sql"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// CartItem mirrors one row of cart_items (migration
// 000018_create_cart_items) — a customer's saved qty of one product
// variant. Persisted server-side, not in localStorage, so it's shared
// between the web and mobile app for the same logged-in customer, per
// web-plan Architecture Decisions.
type CartItem struct {
	CustomerID string
	VariantID  string
	Qty        int
	CreatedAt  time.Time
}

// CartRepo is the read/write contract for a customer's cart.
type CartRepo struct {
	db *sql.DB
}

func NewCartRepo(db *sql.DB) *CartRepo {
	return &CartRepo{db: db}
}

// List returns every line in customerID's cart, oldest-added first.
func (r *CartRepo) List(ctx context.Context, customerID string) ([]CartItem, error) {
	const q = `
		SELECT customer_id, variant_id, qty, created_at
		FROM cart_items
		WHERE customer_id = $1
		ORDER BY created_at`

	rows, err := r.db.QueryContext(ctx, q, customerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	items := []CartItem{}
	for rows.Next() {
		var it CartItem
		if err := rows.Scan(&it.CustomerID, &it.VariantID, &it.Qty, &it.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// Add inserts a line, or increments qty if variantID is already in the
// cart (cart_items' primary key is (customer_id, variant_id), so this is
// an upsert).
func (r *CartRepo) Add(ctx context.Context, customerID, variantID string, qty int) error {
	if variantID == "" {
		return apperr.BadRequest("variant_required", "не указан вариант товара")
	}
	if qty <= 0 {
		return apperr.BadRequest("invalid_qty", "количество должно быть больше нуля")
	}

	const q = `
		INSERT INTO cart_items (customer_id, variant_id, qty)
		VALUES ($1, $2, $3)
		ON CONFLICT (customer_id, variant_id)
		DO UPDATE SET qty = cart_items.qty + EXCLUDED.qty`

	_, err := r.db.ExecContext(ctx, q, customerID, variantID, qty)
	return err
}

// UpdateQty sets the qty for one existing line — backs the cart page's
// stepper. A qty of zero or less removes the line instead of failing, so
// callers can drive "decrement to zero removes the row" purely through
// this one method.
func (r *CartRepo) UpdateQty(ctx context.Context, customerID, variantID string, qty int) error {
	if qty <= 0 {
		return r.Remove(ctx, customerID, variantID)
	}

	const q = `UPDATE cart_items SET qty = $3 WHERE customer_id = $1 AND variant_id = $2`
	res, err := r.db.ExecContext(ctx, q, customerID, variantID, qty)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("cart_item_not_found", "товар не найден в корзине")
	}
	return nil
}

// Remove deletes one line from the cart. Removing a line that isn't there
// is a no-op, not an error, so it stays safe to call from a "decrement to
// zero" path.
func (r *CartRepo) Remove(ctx context.Context, customerID, variantID string) error {
	const q = `DELETE FROM cart_items WHERE customer_id = $1 AND variant_id = $2`
	_, err := r.db.ExecContext(ctx, q, customerID, variantID)
	return err
}
