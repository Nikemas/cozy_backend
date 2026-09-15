// Package storefront holds customer-facing domain logic: customers,
// delivery addresses, favorites.
package storefront

import (
	"context"
	"database/sql"
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
