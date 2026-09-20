package storefront

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
)

// pgForeignKeyViolation is the Postgres SQLSTATE for a foreign-key
// constraint violation (see internal/catalog/pgerr.go for the same
// pattern — kept local here rather than exported cross-package for one
// constant).
const pgForeignKeyViolation = "23503"

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation
}

// Address mirrors one row of customer_addresses (migration
// 000002_create_customer_addresses) — a saved delivery address for a
// customer, picked from on the checkout screen (Task 3) and managed from
// the profile's "addresses" screen (this task).
type Address struct {
	ID          string
	CustomerID  string
	Label       *string // "Дом", "Работа", ...
	AddressText string
	Lat         *float64
	Lng         *float64
	IsDefault   bool
}

// AddressInput is what the create/edit form submits. Label/Lat/Lng are
// optional — only AddressText is required.
type AddressInput struct {
	Label       *string
	AddressText string
	Lat         *float64
	Lng         *float64
	IsDefault   bool
}

// Validate checks AddressText is non-blank, trimming it in place. Pulled
// out as a pure function so it's unit-testable without a database.
func (in *AddressInput) Validate() error {
	in.AddressText = strings.TrimSpace(in.AddressText)
	if in.AddressText == "" {
		return apperr.BadRequest("invalid_address", "укажите адрес доставки")
	}
	if in.Label != nil {
		trimmed := strings.TrimSpace(*in.Label)
		if trimmed == "" {
			in.Label = nil
		} else {
			in.Label = &trimmed
		}
	}
	return nil
}

type AddressRepo struct {
	db *sql.DB
}

func NewAddressRepo(db *sql.DB) *AddressRepo {
	return &AddressRepo{db: db}
}

// List returns customerID's saved addresses, default first, then newest
// first.
func (r *AddressRepo) List(ctx context.Context, customerID string) ([]Address, error) {
	const q = `
		SELECT id, customer_id, label, address_text, lat, lng, is_default
		FROM customer_addresses
		WHERE customer_id = $1
		ORDER BY is_default DESC, created_at DESC`

	rows, err := r.db.QueryContext(ctx, q, customerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	addresses := []Address{}
	for rows.Next() {
		var a Address
		if err := rows.Scan(&a.ID, &a.CustomerID, &a.Label, &a.AddressText, &a.Lat, &a.Lng, &a.IsDefault); err != nil {
			return nil, err
		}
		addresses = append(addresses, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return addresses, nil
}

// GetByID returns one address, scoped to customerID so a customer can't
// fetch (or later edit/delete) another customer's address by guessing an
// ID. Returns apperr.NotFound if it doesn't exist or belongs to someone
// else.
func (r *AddressRepo) GetByID(ctx context.Context, customerID, id string) (*Address, error) {
	const q = `
		SELECT id, customer_id, label, address_text, lat, lng, is_default
		FROM customer_addresses
		WHERE id = $1 AND customer_id = $2`

	var a Address
	err := r.db.QueryRowContext(ctx, q, id, customerID).
		Scan(&a.ID, &a.CustomerID, &a.Label, &a.AddressText, &a.Lat, &a.Lng, &a.IsDefault)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("address_not_found", "адрес не найден")
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Create inserts a new address for customerID. If in.IsDefault, every
// other address of this customer is cleared first (in the same
// transaction) so at most one address is ever the default.
func (r *AddressRepo) Create(ctx context.Context, customerID string, in AddressInput) (*Address, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}

	var a Address
	err := dbtx.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		if in.IsDefault {
			if _, err := tx.ExecContext(ctx, `UPDATE customer_addresses SET is_default = false WHERE customer_id = $1`, customerID); err != nil {
				return err
			}
		}

		const q = `
			INSERT INTO customer_addresses (customer_id, label, address_text, lat, lng, is_default)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, customer_id, label, address_text, lat, lng, is_default`

		return tx.QueryRowContext(ctx, q, customerID, in.Label, in.AddressText, in.Lat, in.Lng, in.IsDefault).
			Scan(&a.ID, &a.CustomerID, &a.Label, &a.AddressText, &a.Lat, &a.Lng, &a.IsDefault)
	})
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Update overwrites an existing address, scoped to customerID. Returns
// apperr.NotFound if id doesn't belong to customerID.
func (r *AddressRepo) Update(ctx context.Context, customerID, id string, in AddressInput) (*Address, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}

	var a Address
	err := dbtx.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		if in.IsDefault {
			if _, err := tx.ExecContext(ctx, `UPDATE customer_addresses SET is_default = false WHERE customer_id = $1 AND id <> $2`, customerID, id); err != nil {
				return err
			}
		}

		const q = `
			UPDATE customer_addresses
			SET label = $3, address_text = $4, lat = $5, lng = $6, is_default = $7
			WHERE id = $1 AND customer_id = $2
			RETURNING id, customer_id, label, address_text, lat, lng, is_default`

		err := tx.QueryRowContext(ctx, q, id, customerID, in.Label, in.AddressText, in.Lat, in.Lng, in.IsDefault).
			Scan(&a.ID, &a.CustomerID, &a.Label, &a.AddressText, &a.Lat, &a.Lng, &a.IsDefault)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFound("address_not_found", "адрес не найден")
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Delete removes an address, scoped to customerID. Returns apperr.NotFound
// if id doesn't belong to customerID.
func (r *AddressRepo) Delete(ctx context.Context, customerID, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM customer_addresses WHERE id = $1 AND customer_id = $2`, id, customerID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return apperr.Conflict("address_in_use", "этот адрес использован в заказе, его нельзя удалить")
		}
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("address_not_found", "адрес не найден")
	}
	return nil
}
