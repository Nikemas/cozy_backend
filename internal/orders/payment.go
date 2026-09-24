package orders

import (
	"context"
	"database/sql"
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

// CreateOnlineOrder is PlaceOrder for payment_method = online_card: the
// same stock locking/decrement and order/items inserts, plus — in the same
// transaction — a pending payments row for provider (amount = order total,
// delivery included, currency KGS). It returns the new payments.id; the
// caller (internal/payments) then asks the provider for a checkout session
// and stores its external id on that row.
//
// Stock is reserved (decremented) at order creation exactly like a
// cash_on_delivery order, so the goods can't be sold twice while the
// customer is on the bank page. If the payment then fails, is cancelled or
// expires, CancelUnpaidOrderTx puts the stock back. Staff are not told
// about the order until it is paid (see NotifyOrderPaid).
//
// created=false means in.IdempotencyKey matched an existing order: that
// order is returned and paymentID is empty.
func (s *Service) CreateOnlineOrder(ctx context.Context, in PlaceOrderInput, provider string) (order *Order, paymentID string, created bool, err error) {
	order, created, err = s.createOrder(ctx, in, PaymentOnlineCard,
		func(tx *sql.Tx, o *Order) error {
			const q = `
				INSERT INTO payments (order_id, provider, status, amount, currency)
				VALUES ($1, $2, 'pending', $3, 'KGS')
				RETURNING id`
			return tx.QueryRowContext(ctx, q, o.ID, provider, o.TotalAmount).Scan(&paymentID)
		})
	if err != nil {
		return nil, "", false, err
	}
	return order, paymentID, created, nil
}
