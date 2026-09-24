package orders

import (
	"context"
	"database/sql"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
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

// MaxPaymentAttempts caps how many payment sessions one order may open
// (the first one plus retries), so a stuck client can't grow the payments
// table without bound or keep the stock reserved forever (every retry
// restarts the PAYMENT_PENDING_TTL clock).
const MaxPaymentAttempts = 10

// ErrPaymentNotRetryable is POST /api/v1/orders/{id}/pay on an order that
// can't take a new payment attempt.
var ErrPaymentNotRetryable = apperr.Conflict("payment_not_retryable",
	"этот заказ нельзя оплатить повторно")

// PaymentRetryable reports whether o can take a new online payment
// attempt: an online_card order that is still 'placed' (so not cancelled)
// whose payment is pending (the customer left the bank page) or failed
// (declined).
func PaymentRetryable(o Order) bool {
	if o.PaymentMethod != PaymentOnlineCard || o.Status != StatusPlaced || o.PaymentStatus == nil {
		return false
	}
	return *o.PaymentStatus == PaymentPending || *o.PaymentStatus == PaymentFailed
}

// LockOrderTx loads orderID FOR UPDATE inside the caller's transaction.
// Everything that changes an online order's payments locks the order row
// first and its payments rows second (retry, callback, expiry, cancel), so
// they serialize on the order and never deadlock on each other.
func LockOrderTx(ctx context.Context, tx *sql.Tx, orderID string) (*Order, error) {
	return lockOrderTx(ctx, tx, "id = $1", orderID)
}

// PrepareRetryPayment opens a new payment attempt on customerID's own
// order idOrNumber: under the order's row lock it re-checks
// PaymentRetryable (409 payment_not_retryable otherwise), closes the
// previous still-pending payment ('cancelled' — a late "paid" for it is
// still honoured while the order is unpaid, see payments.HandleCallback),
// inserts a new pending payments row for provider (amount = order total)
// and sets orders.payment_status back to pending. It returns the order and
// the new payments.id; the caller opens the provider session for it.
func (s *Service) PrepareRetryPayment(ctx context.Context, customerID, idOrNumber, provider string) (*Order, string, error) {
	var o *Order
	var paymentID string
	err := dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		o, err = lockOrderTx(ctx, tx, "customer_id = $1 AND "+orderKeyPredicate(idOrNumber, 2), customerID, idOrNumber)
		if err != nil {
			return err
		}
		if !PaymentRetryable(*o) {
			return ErrPaymentNotRetryable
		}
		var attempts int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM payments WHERE order_id = $1`, o.ID).Scan(&attempts); err != nil {
			return err
		}
		if attempts >= MaxPaymentAttempts {
			return apperr.Conflict("payment_not_retryable",
				"слишком много попыток оплаты — оформите заказ заново или выберите оплату при получении")
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE payments SET status = 'cancelled', updated_at = now() WHERE order_id = $1 AND status = 'pending'`,
			o.ID); err != nil {
			return err
		}
		const insQ = `
			INSERT INTO payments (order_id, provider, status, amount, currency)
			VALUES ($1, $2, 'pending', $3, 'KGS')
			RETURNING id`
		if err := tx.QueryRowContext(ctx, insQ, o.ID, provider, o.TotalAmount).Scan(&paymentID); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx,
			`UPDATE orders SET payment_status = 'pending', updated_at = now() WHERE id = $1 RETURNING updated_at`,
			o.ID).Scan(&o.UpdatedAt); err != nil {
			return err
		}
		pending := PaymentPending
		o.PaymentStatus = &pending
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return o, paymentID, nil
}
