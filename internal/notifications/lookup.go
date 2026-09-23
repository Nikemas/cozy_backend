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
