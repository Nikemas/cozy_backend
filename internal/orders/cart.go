// Package orders holds the cart and order lifecycle: cart_items, orders,
// order_items. Task 1 (Foundation) froze the types and method signatures
// below so internal/web (Tasks 2, 4, 5) could be built against a stable
// contract while Task 3 implemented the SQL bodies (this file/order.go).
package orders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// CartItem mirrors one row of cart_items (migration
// 000018_create_cart_items) — a customer's saved qty of one product
// variant. Persisted server-side, not in localStorage, so it's shared
// between the web and mobile app for the same logged-in customer, per
// web-plan Architecture Decisions.
type CartItem struct {
	CustomerID string    `json:"customer_id"`
	VariantID  string    `json:"variant_id"`
	Qty        int       `json:"qty"`
	CreatedAt  time.Time `json:"created_at"`
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

// CartLine is one cart_items row joined with everything a cart screen
// needs (product, variant, current price, availability) — read in one
// query instead of a variant+product lookup per line.
type CartLine struct {
	VariantID     string
	Qty           int
	ProductID     string
	ProductName   string // name_ru
	ProductNameKy string
	Size          string
	Color         string
	Price         float64 // price_override, else base_price
	// ProductActive is false once the product was deactivated: the line
	// stays visible (the customer picked it) but can't be ordered.
	ProductActive bool
	// InStock is the most any single active point holds of this variant —
	// the largest quantity one order can take.
	InStock   int
	CreatedAt time.Time
}

// Available reports whether the line can be ordered as is.
func (l CartLine) Available() bool { return l.ProductActive && l.InStock >= l.Qty && l.Qty > 0 }

// ListDetailed returns customerID's cart lines with product/variant data,
// oldest-added first. Lines whose variant was hard-deleted are gone by
// ON DELETE CASCADE, so every row joins.
func (r *CartRepo) ListDetailed(ctx context.Context, customerID string) ([]CartLine, error) {
	const q = `
		SELECT ci.variant_id, ci.qty, p.id, p.name_ru, p.name_ky, pv.size, pv.color,
		       COALESCE(pv.price_override, p.base_price), p.is_active,
		       COALESCE((SELECT MAX(s.quantity) FROM stock s
		                 JOIN points_of_sale ps ON ps.id = s.point_id AND ps.is_active = true
		                 WHERE s.variant_id = ci.variant_id), 0),
		       ci.created_at
		FROM cart_items ci
		JOIN product_variants pv ON pv.id = ci.variant_id
		JOIN products p ON p.id = pv.product_id
		WHERE ci.customer_id = $1
		ORDER BY ci.created_at`

	rows, err := r.db.QueryContext(ctx, q, customerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	lines := []CartLine{}
	for rows.Next() {
		var l CartLine
		if err := rows.Scan(&l.VariantID, &l.Qty, &l.ProductID, &l.ProductName, &l.ProductNameKy, &l.Size, &l.Color,
			&l.Price, &l.ProductActive, &l.InStock, &l.CreatedAt); err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

// validateCartVariantID rejects a malformed id up front — it would
// otherwise reach Postgres as an invalid uuid literal and come back as 500.
func validateCartVariantID(variantID string) error {
	if variantID == "" {
		return apperr.BadRequest("variant_required", "не указан вариант товара")
	}
	if uuid.Validate(variantID) != nil {
		return apperr.BadRequest("invalid_variant_id", "некорректный идентификатор варианта товара")
	}
	return nil
}

func validateCartQty(qty int) error {
	if qty <= 0 {
		return apperr.BadRequest("invalid_qty", "количество должно быть больше нуля")
	}
	if qty > MaxCartQty {
		return apperr.BadRequest("qty_too_large", fmt.Sprintf("не больше %d шт. одного товара", MaxCartQty))
	}
	return nil
}

// Add inserts a line, or increments qty if variantID is already in the
// cart (cart_items' primary key is (customer_id, variant_id), so this is
// an upsert). The line total is capped at MaxCartQty. Unknown variants are
// 404 variant_not_found and deactivated products 409 product_unavailable,
// instead of a foreign-key 500.
func (r *CartRepo) Add(ctx context.Context, customerID, variantID string, qty int) error {
	if err := validateCartVariantID(variantID); err != nil {
		return err
	}
	if err := validateCartQty(qty); err != nil {
		return err
	}

	var active bool
	err := r.db.QueryRowContext(ctx,
		`SELECT p.is_active FROM product_variants pv JOIN products p ON p.id = pv.product_id WHERE pv.id = $1`,
		variantID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("variant_not_found", "товар не найден")
	}
	if err != nil {
		return err
	}
	if !active {
		return apperr.Conflict("product_unavailable", "товар больше не продаётся")
	}

	const q = `
		INSERT INTO cart_items (customer_id, variant_id, qty)
		VALUES ($1, $2, $3)
		ON CONFLICT (customer_id, variant_id)
		DO UPDATE SET qty = LEAST(cart_items.qty + EXCLUDED.qty, $4)`

	_, err = r.db.ExecContext(ctx, q, customerID, variantID, qty, MaxCartQty)
	return err
}

// UpdateQty sets the qty for one existing line — backs the cart page's
// stepper. A qty of zero or less removes the line instead of failing, so
// callers can drive "decrement to zero removes the row" purely through
// this one method.
func (r *CartRepo) UpdateQty(ctx context.Context, customerID, variantID string, qty int) error {
	if err := validateCartVariantID(variantID); err != nil {
		return err
	}
	if qty <= 0 {
		return r.Remove(ctx, customerID, variantID)
	}
	if err := validateCartQty(qty); err != nil {
		return err
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
	if err := validateCartVariantID(variantID); err != nil {
		return err
	}
	const q = `DELETE FROM cart_items WHERE customer_id = $1 AND variant_id = $2`
	_, err := r.db.ExecContext(ctx, q, customerID, variantID)
	return err
}
