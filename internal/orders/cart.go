// Package orders holds the cart and order lifecycle: cart_items, orders,
// order_items. Task 1 (Foundation) freezes the types and method
// signatures below so internal/web (Tasks 2, 4, 5) can be built against a
// stable contract while Task 3 implements the SQL bodies. Every method
// currently returns a 501 apperr — swap the body for real SQL (including
// the withTx/SELECT...FOR UPDATE stock-decrement pattern from the tech
// spec) without touching any caller.
package orders

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// CartItem mirrors one row of cart_items (migration
// 000017_create_cart_items) — a customer's saved qty of one product
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

// List returns every line in customerID's cart.
func (r *CartRepo) List(ctx context.Context, customerID string) ([]CartItem, error) {
	return nil, apperr.New(http.StatusNotImplemented, "not_implemented", "orders.CartRepo.List: not implemented")
}

// Add inserts a line, or increments qty if variantID is already in the
// cart (cart_items' primary key is (customer_id, variant_id), so this is
// an upsert).
func (r *CartRepo) Add(ctx context.Context, customerID, variantID string, qty int) error {
	return apperr.New(http.StatusNotImplemented, "not_implemented", "orders.CartRepo.Add: not implemented")
}

// UpdateQty sets the qty for one existing line — backs the cart page's
// stepper.
func (r *CartRepo) UpdateQty(ctx context.Context, customerID, variantID string, qty int) error {
	return apperr.New(http.StatusNotImplemented, "not_implemented", "orders.CartRepo.UpdateQty: not implemented")
}

// Remove deletes one line from the cart.
func (r *CartRepo) Remove(ctx context.Context, customerID, variantID string) error {
	return apperr.New(http.StatusNotImplemented, "not_implemented", "orders.CartRepo.Remove: not implemented")
}
