// Package storefront holds customer-facing domain logic: customers,
// delivery addresses, favorites.
package storefront

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

type Customer struct {
	ID    string
	Phone string
	Name  *string
}

type CustomerRepo struct {
	db *sql.DB
}

func NewCustomerRepo(db *sql.DB) *CustomerRepo {
	return &CustomerRepo{db: db}
}

// GetOrCreateByPhone returns the existing customer for phone, or creates
// one. Used by the OTP login flow, where a verified phone number is the
// only signal we have.
func (r *CustomerRepo) GetOrCreateByPhone(ctx context.Context, phone string) (*Customer, error) {
	const q = `
		INSERT INTO customers (phone) VALUES ($1)
		ON CONFLICT (phone) DO UPDATE SET phone = EXCLUDED.phone
		RETURNING id, phone, name`

	var c Customer
	err := r.db.QueryRowContext(ctx, q, phone).Scan(&c.ID, &c.Phone, &c.Name)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// SetName records name for an existing customer — used by the web login
// flow's third step ("как вас зовут") for a phone number that just
// verified its OTP but has no name on file yet.
func (r *CustomerRepo) SetName(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return apperr.BadRequest("invalid_name", "укажите имя")
	}

	const q = `UPDATE customers SET name = $2, updated_at = now() WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id, name)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("customer_not_found", "покупатель не найден")
	}
	return nil
}

// GetByID returns the customer with id, or nil if none exists. Used by
// the web session middleware to resolve a session cookie's customer_id
// into profile data (name, phone) for the header/aside account card and
// the profile screen.
func (r *CustomerRepo) GetByID(ctx context.Context, id string) (*Customer, error) {
	const q = `SELECT id, phone, name FROM customers WHERE id = $1`

	var c Customer
	err := r.db.QueryRowContext(ctx, q, id).Scan(&c.ID, &c.Phone, &c.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}
