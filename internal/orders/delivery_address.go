package orders

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// OrderDeliveryAddress is the delivery address as it was at checkout
// (orders.delivery_address_label/_text, migration 000045) — copied from
// customer_addresses in the order's own transaction, so a later edit of
// the saved address doesn't rewrite order history.
type OrderDeliveryAddress struct {
	Label       *string `json:"label"`
	AddressText string  `json:"address_text"`
}

// loadAddressSnapshot checks that addressID is customerID's and returns
// its current label/text for the order's snapshot. A blank label becomes
// nil so the snapshot never carries "".
func loadAddressSnapshot(ctx context.Context, tx *sql.Tx, customerID, addressID string) (*OrderDeliveryAddress, error) {
	const q = `SELECT label, address_text FROM customer_addresses WHERE id = $1 AND customer_id = $2`
	var label, text sql.NullString
	err := tx.QueryRowContext(ctx, q, addressID, customerID).Scan(&label, &text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("address_not_found", "адрес не найден")
	}
	if err != nil {
		return nil, err
	}
	addr := deliveryAddressFromSnapshot(label, text)
	if addr == nil {
		// address_text is NOT NULL, so this only guards a broken row.
		return nil, apperr.NotFound("address_not_found", "адрес не найден")
	}
	return addr, nil
}

// deliveryAddressFromSnapshot builds the API value from the two nullable
// snapshot columns: nil when there is no snapshot (pickup orders, or a
// delivery order whose address was gone before migration 000045's
// backfill).
func deliveryAddressFromSnapshot(label, text sql.NullString) *OrderDeliveryAddress {
	if !text.Valid {
		return nil
	}
	addr := &OrderDeliveryAddress{AddressText: text.String}
	if l := strings.TrimSpace(label.String); label.Valid && l != "" {
		addr.Label = &l
	}
	return addr
}

// snapshotColumns returns the values for the orders insert's
// delivery_address_label/_text (both nil for pickup).
func (a *OrderDeliveryAddress) snapshotColumns() (label, text *string) {
	if a == nil {
		return nil, nil
	}
	return a.Label, &a.AddressText
}
