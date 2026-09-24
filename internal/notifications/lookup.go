package notifications

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

// SQLOrderInfoLookup reads customer/address/point display data for the
// staff message straight from Postgres, after the order has committed.
type SQLOrderInfoLookup struct {
	db *sql.DB
}

func NewSQLOrderInfoLookup(db *sql.DB) *SQLOrderInfoLookup {
	return &SQLOrderInfoLookup{db: db}
}

func (l *SQLOrderInfoLookup) OrderInfo(ctx context.Context, o orders.Order) (OrderInfo, error) {
	const q = `
		SELECT COALESCE(c.name, ''), c.phone,
		       COALESCE(a.address_text, ''),
		       COALESCE(p.name, ''), COALESCE(p.address, '')
		FROM customers c
		LEFT JOIN customer_addresses a ON a.id = $2
		LEFT JOIN points_of_sale p ON p.id = $3
		WHERE c.id = $1`

	var info OrderInfo
	err := l.db.QueryRowContext(ctx, q, o.CustomerID, o.AddressID, o.PointID).
		Scan(&info.CustomerName, &info.CustomerPhone, &info.AddressText, &info.PointName, &info.PointAddress)
	if errors.Is(err, sql.ErrNoRows) {
		return OrderInfo{}, nil
	}
	return info, err
}

// SQLContactLookup reads a customer's language and phone from customers.
// A deleted (anonymized) customer yields an empty phone, so no SMS goes to
// the 'deleted:<id>' placeholder.
type SQLContactLookup struct {
	db *sql.DB
}

func NewSQLContactLookup(db *sql.DB) *SQLContactLookup {
	return &SQLContactLookup{db: db}
}

func (l *SQLContactLookup) CustomerContact(ctx context.Context, customerID string) (CustomerContact, error) {
	const q = `
		SELECT lang, CASE WHEN deleted_at IS NULL THEN phone ELSE '' END
		FROM customers WHERE id = $1`
	var c CustomerContact
	err := l.db.QueryRowContext(ctx, q, customerID).Scan(&c.Lang, &c.Phone)
	if errors.Is(err, sql.ErrNoRows) {
		return CustomerContact{}, nil
	}
	return c, err
}
