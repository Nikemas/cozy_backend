package orders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// PaymentStatus mirrors the payment_status enum (000012, extended with
// 'cancelled' by 000021). It lives here rather than in internal/payments
// because Order carries it (orders.payment_status) and internal/payments
// already imports this package — payments.Status is an alias of it.
type PaymentStatus string

const (
	PaymentPending   PaymentStatus = "pending"
	PaymentPaid      PaymentStatus = "paid"
	PaymentFailed    PaymentStatus = "failed"
	PaymentCancelled PaymentStatus = "cancelled"
	PaymentRefunded  PaymentStatus = "refunded"
)

// Valid reports whether s is one of the payment_status enum values.
func (s PaymentStatus) Valid() bool {
	switch s {
	case PaymentPending, PaymentPaid, PaymentFailed, PaymentCancelled, PaymentRefunded:
		return true
	}
	return false
}

// CreateOnlineOrder is CreateOrder for payment_method = online_card: the
// same stock locking/decrement and order/items inserts, plus — in the same
// transaction — a pending payments row for provider (amount = order total,
// currency KGS). It returns the new payments.id; the caller (internal/
// payments) then asks the provider for a checkout session and stores its
// external id on that row.
//
// Stock is reserved (decremented) at order creation exactly like a
// cash_on_delivery order, so the goods can't be sold twice while the
// customer is on the bank page. If the payment then fails or is cancelled,
// CancelUnpaidOrderTx puts the stock back.
func (s *Service) CreateOnlineOrder(ctx context.Context, customerID string, items []OrderItemInput, addressID, pickupPointID *string, provider string) (*Order, string, error) {
	var paymentID string
	order, err := s.createOrder(ctx, customerID, items, addressID, pickupPointID, PaymentOnlineCard,
		func(tx *sql.Tx, o *Order) error {
			const q = `
				INSERT INTO payments (order_id, provider, status, amount, currency)
				VALUES ($1, $2, 'pending', $3, 'KGS')
				RETURNING id`
			return tx.QueryRowContext(ctx, q, o.ID, provider, o.TotalAmount).Scan(&paymentID)
		})
	if err != nil {
		return nil, "", err
	}
	return order, paymentID, nil
}

// CancelUnpaidOrderTx cancels orderID and returns its reserved stock to
// the order's fulfillment point, inside the caller's transaction. It is
// the compensating action for a failed/cancelled online payment.
//
// It only acts on an order still in status 'placed' (locked FOR UPDATE):
// once staff have confirmed/assigned it, or it's already cancelled, it
// returns (false, nil) and touches nothing: after 'placed' the order is in
// staff hands, and whether an admin-cancelled order's stock goes back is
// AdminUpdateStatus's call (today it doesn't restock at all), not the
// payment webhook's. This also makes a repeated failure callback unable to
// restock twice. Stock rows are updated in variant-id
// order, the same lock order CreateOrder uses, so the two can't deadlock.
func CancelUnpaidOrderTx(ctx context.Context, tx *sql.Tx, orderID string) (bool, error) {
	var status OrderStatus
	var pointID sql.NullString
	const lockQ = `SELECT status, point_id FROM orders WHERE id = $1 FOR UPDATE`
	if err := tx.QueryRowContext(ctx, lockQ, orderID).Scan(&status, &pointID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, apperr.NotFound("order_not_found", "заказ не найден")
		}
		return false, err
	}
	if status != StatusPlaced {
		return false, nil
	}
	if !pointID.Valid {
		// CreateOrder always records the fulfillment point, so this means
		// the row was edited by hand; refuse rather than lose stock silently.
		return false, fmt.Errorf("orders: order %s has no point_id, cannot return stock", orderID)
	}

	const itemsQ = `SELECT variant_id, quantity FROM order_items WHERE order_id = $1`
	rows, err := tx.QueryContext(ctx, itemsQ, orderID)
	if err != nil {
		return false, err
	}
	qtyByVariant := map[string]int{}
	for rows.Next() {
		var variantID string
		var qty int
		if err := rows.Scan(&variantID, &qty); err != nil {
			_ = rows.Close()
			return false, err
		}
		qtyByVariant[variantID] += qty
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	_ = rows.Close()

	variantIDs := make([]string, 0, len(qtyByVariant))
	for id := range qtyByVariant {
		variantIDs = append(variantIDs, id)
	}
	sort.Strings(variantIDs)

	// Upsert rather than UPDATE: the stock row could have been deleted by
	// an admin since the order was placed; the returned goods still exist.
	const restockQ = `
		INSERT INTO stock (variant_id, point_id, quantity, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (variant_id, point_id)
		DO UPDATE SET quantity = stock.quantity + EXCLUDED.quantity, updated_at = now()`
	for _, variantID := range variantIDs {
		if _, err := tx.ExecContext(ctx, restockQ, variantID, pointID.String, qtyByVariant[variantID]); err != nil {
			return false, err
		}
	}

	const cancelQ = `UPDATE orders SET status = 'cancelled', updated_at = now() WHERE id = $1`
	if _, err := tx.ExecContext(ctx, cancelQ, orderID); err != nil {
		return false, err
	}
	return true, nil
}
