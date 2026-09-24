// Package storefront holds customer-facing domain logic: customers,
// delivery addresses, favorites.
package storefront

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

type Customer struct {
	ID    string
	Phone string
	Name  *string
	// Lang is the customer's preferred language (i18n.LangRU/LangKY,
	// customers.lang, migration 000032) — order-status pushes and promo
	// broadcasts are rendered in it.
	Lang string
	// PromoPush is the "Акции и скидки" opt-in for promo broadcasts
	// (default true). Order-status pushes ignore it.
	PromoPush bool
}

// customerColumns is the SELECT/RETURNING list every Customer scan uses,
// in scanCustomer's order.
const customerColumns = `id, phone, name, lang, promo_push`

type rowScanner interface{ Scan(dest ...any) error }

func scanCustomer(row rowScanner) (*Customer, error) {
	var c Customer
	if err := row.Scan(&c.ID, &c.Phone, &c.Name, &c.Lang, &c.PromoPush); err != nil {
		return nil, err
	}
	return &c, nil
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
		RETURNING ` + customerColumns

	return scanCustomer(r.db.QueryRowContext(ctx, q, phone))
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
	const q = `SELECT ` + customerColumns + ` FROM customers WHERE id = $1`

	c, err := scanCustomer(r.db.QueryRowContext(ctx, q, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ProfileUpdate is a partial update of the customer's own profile: nil
// fields are left unchanged (PUT /api/v1/customer accepts any subset).
type ProfileUpdate struct {
	Name      *string
	Lang      *string
	PromoPush *bool
}

// IsEmpty reports whether u changes nothing.
func (u ProfileUpdate) IsEmpty() bool {
	return u.Name == nil && u.Lang == nil && u.PromoPush == nil
}

// ValidLang reports whether lang is a language customers.lang accepts.
func ValidLang(lang string) bool {
	return lang == i18n.LangRU || lang == i18n.LangKY
}

// validate trims Name in place and checks every set field.
func (u *ProfileUpdate) validate() error {
	if u.Name != nil {
		name := strings.TrimSpace(*u.Name)
		if name == "" {
			return apperr.BadRequest("invalid_name", "укажите имя")
		}
		if utf8.RuneCountInString(name) > maxNameLen {
			return apperr.BadRequest("invalid_name", "имя слишком длинное")
		}
		u.Name = &name
	}
	if u.Lang != nil && !ValidLang(*u.Lang) {
		return apperr.BadRequest("invalid_lang", "lang должен быть ru или ky")
	}
	return nil
}

// maxNameLen caps a customer's display name (it is shown in staff
// Telegram messages and the admin panel).
const maxNameLen = 100

// UpdateProfile applies u to customer id in one UPDATE and returns the
// updated row. An empty update just returns the current row.
func (r *CustomerRepo) UpdateProfile(ctx context.Context, id string, u ProfileUpdate) (*Customer, error) {
	if err := u.validate(); err != nil {
		return nil, err
	}
	if u.IsEmpty() {
		c, err := r.GetByID(ctx, id)
		if err == nil && c == nil {
			return nil, apperr.NotFound("customer_not_found", "покупатель не найден")
		}
		return c, err
	}

	const q = `
		UPDATE customers SET
			name       = COALESCE($2, name),
			lang       = COALESCE($3, lang),
			promo_push = COALESCE($4, promo_push),
			updated_at = now()
		WHERE id = $1
		RETURNING ` + customerColumns

	c, err := scanCustomer(r.db.QueryRowContext(ctx, q, id, u.Name, u.Lang, u.PromoPush))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("customer_not_found", "покупатель не найден")
	}
	return c, err
}

// SetLang stores lang for customer id — the storefront's language switch
// calls it for a logged-in visitor so pushes follow the site language.
// An unknown customer is not an error (the cookie is still set).
func (r *CustomerRepo) SetLang(ctx context.Context, id, lang string) error {
	if !ValidLang(lang) {
		return apperr.BadRequest("invalid_lang", "lang должен быть ru или ky")
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE customers SET lang = $2, updated_at = now() WHERE id = $1 AND lang <> $2`, id, lang)
	return err
}
