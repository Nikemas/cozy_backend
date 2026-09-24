package payments

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// OnlineOrderCreator is the subset of *orders.Service Service needs.
type OnlineOrderCreator interface {
	CreateOnlineOrder(ctx context.Context, in orders.PlaceOrderInput, provider string) (order *orders.Order, paymentID string, created bool, err error)
	// NotifyOrderPaid sends staff the "new order" message for a paid
	// online order (held back at creation until the money is in).
	NotifyOrderPaid(ctx context.Context, orderID string) error
	// PrepareRetryPayment opens a new pending payment attempt on the
	// customer's own order (see orders.Service.PrepareRetryPayment).
	PrepareRetryPayment(ctx context.Context, customerID, idOrNumber, provider string) (*orders.Order, string, error)
}

// Service ties orders to the active Provider: it opens a payment for a new
// online_card order and applies provider callbacks to payments/orders.
//
// Stock handling (the "don't double-handle stock" decision):
//   - An online_card order reserves stock exactly like cash_on_delivery —
//     decremented under SELECT ... FOR UPDATE when the order is created
//     (orders.CreateOnlineOrder), not when the payment succeeds. Otherwise
//     two customers could both pay for the last pair.
//   - paid: nothing happens to stock; the reservation simply stands.
//   - failed (declined): the order stays 'placed' with payment_status
//     failed and its stock reserved, so the customer can pay again on the
//     same order (RetryPayment, POST /api/v1/orders/{id}/pay).
//   - cancelled (customer cancelled on the bank page), or the provider
//     couldn't even open the first session: the order is cancelled and its
//     stock returned, once, in the same transaction as the payment update
//     (orders.CancelUnpaidOrderTx) — only if the order is still 'placed'.
//   - Every retry is a new payments row; the previous pending one is
//     closed ('cancelled'). A late "paid" for such an earlier attempt is
//     still accepted while the order is open and unpaid.
//   - Duplicate callbacks are no-ops (payment row locked FOR UPDATE, same
//     status ⇒ nothing applied), so stock is never returned twice.
//   - Abandoned orders (latest attempt still pending, or failed and never
//     retried) are cancelled by the expiry job (ExpirePending /
//     RunPendingExpiry) PAYMENT_PENDING_TTL after that latest attempt,
//     which returns the stock the same way.
//   - "paid" arriving for an order that is already cancelled or already
//     paid by another attempt sets orders.refund_required.
//
// Lock order: the order row first, then its payments rows — callbacks,
// retries, the expiry job and cancellations all follow it, so they
// serialize on the order instead of deadlocking.
type Service struct {
	db            *sql.DB
	provider      Provider
	orders        OnlineOrderCreator
	publicBaseURL string
}

func NewService(db *sql.DB, provider Provider, ordersSvc OnlineOrderCreator, publicBaseURL string) *Service {
	return &Service{db: db, provider: provider, orders: ordersSvc, publicBaseURL: publicBaseURL}
}

// Provider returns the active provider.
func (s *Service) Provider() Provider { return s.provider }

// PlaceOnlineOrder creates an online_card order (reserving stock and a
// pending payment in one transaction), then opens a checkout session with
// the provider and returns the order plus the URL to send the customer to.
// If the provider fails, the order is cancelled and its stock returned
// before the error is reported.
//
// created=false is an idempotent replay (in.IdempotencyKey matched an
// earlier order): no new order or payment is made, and the earlier
// payment's URL is returned while that payment is still pending.
func (s *Service) PlaceOnlineOrder(ctx context.Context, in orders.PlaceOrderInput) (*orders.Order, string, bool, error) {
	if err := s.provider.Ready(); err != nil {
		return nil, "", false, err
	}

	order, paymentID, created, err := s.orders.CreateOnlineOrder(ctx, in, s.provider.Name())
	if err != nil {
		return nil, "", false, err
	}
	if !created {
		url, err := s.pendingPaymentURL(ctx, order.ID)
		if err != nil {
			return nil, "", false, err
		}
		return order, url, false, nil
	}

	url, err := s.openSession(ctx, order, paymentID)
	if err != nil {
		slog.Error("payments: opening checkout session failed, cancelling order",
			"provider", s.provider.Name(), "order", order.OrderNumber, "err", err)
		// Detached context: the compensation must run even if the client
		// has already gone away.
		if abortErr := s.abort(context.WithoutCancel(ctx), paymentID, order.ID); abortErr != nil {
			slog.Error("payments: compensating order cancel failed — stock stays reserved",
				"order", order.OrderNumber, "err", abortErr)
		}
		return nil, "", false, sessionError(err)
	}
	return order, url, true, nil
}

// ReturnPath is where the provider sends the customer back after paying
// for orderID: the site's result page (internal/web), which shows the
// payment status and, for app users, links back into the app.
func ReturnPath(orderID string) string { return "/pay/return/" + orderID }

// ReturnURL is ReturnPath on publicBaseURL (PUBLIC_BASE_URL).
func (s *Service) ReturnURL(orderID string) string { return s.publicBaseURL + ReturnPath(orderID) }

// openSession asks the provider for a checkout session for payment
// paymentID of order and stores its external id and URL on the row.
func (s *Service) openSession(ctx context.Context, order *orders.Order, paymentID string) (string, error) {
	sess, err := s.provider.CreatePayment(ctx, CreateRequest{
		PaymentID:   paymentID,
		OrderID:     order.ID,
		OrderNumber: order.OrderNumber,
		Amount:      order.TotalAmount,
		Currency:    Currency,
		ReturnURL:   s.ReturnURL(order.ID),
	})
	if err != nil {
		return "", err
	}
	const q = `UPDATE payments SET provider_tx_id = $1, redirect_url = $2, updated_at = now() WHERE id = $3`
	if _, err := s.db.ExecContext(ctx, q, sess.ExternalID, sess.RedirectURL, paymentID); err != nil {
		return "", err
	}
	return sess.RedirectURL, nil
}

// sessionError is what the customer sees when a checkout session couldn't
// be opened: the provider's own AppError, else 502 payment_create_failed.
func sessionError(err error) error {
	var appErr *apperr.AppError
	if errors.As(err, &appErr) {
		return err
	}
	return apperr.New(http.StatusBadGateway, "payment_create_failed", "не удалось создать платёж, попробуйте ещё раз")
}

// RetryPayment opens a new payment attempt on customerID's own online
// order orderID (UUID or order number) and returns the URL to send the
// customer to. Only an online_card order that is still 'placed' with a
// pending or failed payment qualifies (409 payment_not_retryable
// otherwise); the previous pending attempt is closed. If the provider
// can't open the session, the new attempt is marked failed — the order
// stays open for another try (or the expiry job) — and 502
// payment_create_failed is returned.
func (s *Service) RetryPayment(ctx context.Context, customerID, orderID string) (string, error) {
	if err := s.provider.Ready(); err != nil {
		return "", err
	}
	order, paymentID, err := s.orders.PrepareRetryPayment(ctx, customerID, orderID, s.provider.Name())
	if err != nil {
		return "", err
	}
	url, err := s.openSession(ctx, order, paymentID)
	if err != nil {
		slog.Error("payments: opening retry checkout session failed",
			"provider", s.provider.Name(), "order", order.OrderNumber, "err", err)
		if ferr := s.failAttempt(context.WithoutCancel(ctx), paymentID, order.ID); ferr != nil {
			slog.Error("payments: marking the failed retry attempt failed", "order", order.OrderNumber, "err", ferr)
		}
		return "", sessionError(err)
	}
	return url, nil
}

// failAttempt marks a retry attempt whose session never opened as failed
// (the order stays open and retryable).
func (s *Service) failAttempt(ctx context.Context, paymentID, orderID string) error {
	return dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := orders.LockOrderTx(ctx, tx, orderID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE payments SET status = 'failed', updated_at = now() WHERE id = $1 AND status = 'pending'`, paymentID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE orders SET payment_status = 'failed', updated_at = now() WHERE id = $1 AND status = 'placed'`, orderID)
		return err
	})
}

// pendingPaymentURL is the checkout URL of orderID's latest payment while
// it is still pending, or "" (paid, failed, expired — nothing to open).
func (s *Service) pendingPaymentURL(ctx context.Context, orderID string) (string, error) {
	const q = `
		SELECT status, COALESCE(redirect_url, '') FROM payments
		WHERE order_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`
	var st Status
	var url string
	err := s.db.QueryRowContext(ctx, q, orderID).Scan(&st, &url)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if st != StatusPending {
		return "", nil
	}
	return url, nil
}

// abort marks a still-pending payment failed and cancels its order,
// returning stock. The order's idempotency key is released so the
// client's retry with the same key places a fresh order instead of
// replaying this cancelled one.
func (s *Service) abort(ctx context.Context, paymentID, orderID string) error {
	return dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := orders.LockOrderTx(ctx, tx, orderID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE payments SET status = 'failed', updated_at = now() WHERE id = $1 AND status = 'pending'`, paymentID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // a callback already settled it
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE orders SET payment_status = 'failed', idempotency_key = NULL, updated_at = now() WHERE id = $1`, orderID); err != nil {
			return err
		}
		_, err = orders.CancelUnpaidOrderTx(ctx, tx, orderID, "не удалось открыть платёж")
		return err
	})
}

// CallbackResult reports what HandleCallback did.
type CallbackResult struct {
	PaymentID      string `json:"payment_id"`
	OrderID        string `json:"order_id"`
	Status         Status `json:"status"`          // payment status after handling
	Applied        bool   `json:"applied"`         // false for duplicates / ignored events
	OrderCancelled bool   `json:"order_cancelled"` // stock returned
	RefundRequired bool   `json:"refund_required"` // money arrived for a cancelled order
}

// transition decides what a callback reporting `to` does to a payment
// currently in `from`. apply=false with conflict=false is a harmless
// duplicate/progress event; conflict=true is an out-of-order or
// contradictory event (e.g. "paid" after we already cancelled the order)
// that needs a human — it is logged, not applied.
func transition(from, to Status) (apply, conflict bool) {
	switch {
	case to == StatusPending || from == to:
		return false, false
	case isUnpaidFinal(from) && isUnpaidFinal(to):
		// failed vs cancelled: both already cancelled the order; nothing
		// to reconcile.
		return false, false
	case from == StatusPending && (to == StatusPaid || to == StatusFailed || to == StatusCancelled):
		return true, false
	case from == StatusPaid && to == StatusRefunded:
		return true, false
	default:
		return false, true
	}
}

func isUnpaidFinal(s Status) bool { return s == StatusFailed || s == StatusCancelled }

// lateSuccess: a "paid" callback for an attempt that is already failed or
// cancelled (closed by a retry), while its order is still 'placed' and not
// paid — accepted rather than refunded, since the order is still waiting
// for exactly this money.
func lateSuccess(current, to Status, o *orders.Order) bool {
	if to != StatusPaid || !isUnpaidFinal(current) || o == nil {
		return false
	}
	return orders.PaymentRetryable(*o)
}

// HandleCallback authenticates and applies one provider callback for
// providerName. It is idempotent: the payment row is locked FOR UPDATE and
// a repeated event is a no-op. The payment update, orders.payment_status
// update and (on failure) the order cancel + stock return all commit
// together or not at all.
func (s *Service) HandleCallback(ctx context.Context, providerName string, header http.Header, body []byte) (*CallbackResult, error) {
	if providerName != s.provider.Name() {
		return nil, apperr.NotFound("payment_provider_not_found", "платёжный провайдер не найден")
	}
	ev, err := s.provider.ParseCallback(header, body)
	if err != nil {
		return nil, err
	}
	raw := ev.Raw
	if !json.Valid(raw) {
		raw, _ = json.Marshal(string(ev.Raw))
	}

	var res CallbackResult
	err = dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		// payments.order_id never changes, so finding the order unlocked
		// is safe; then lock the order and the payment, in that order.
		const findQ = `SELECT id, order_id FROM payments WHERE provider = $1 AND provider_tx_id = $2`
		err := tx.QueryRowContext(ctx, findQ, providerName, ev.ExternalID).Scan(&res.PaymentID, &res.OrderID)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFound("payment_not_found", "платёж не найден")
		}
		if err != nil {
			return err
		}
		order, err := orders.LockOrderTx(ctx, tx, res.OrderID)
		if err != nil {
			return err
		}
		var current Status
		var amount float64
		if err := tx.QueryRowContext(ctx,
			`SELECT status, amount FROM payments WHERE id = $1 FOR UPDATE`, res.PaymentID).Scan(&current, &amount); err != nil {
			return err
		}
		res.Status = current

		apply, conflict := transition(current, ev.Status)
		if conflict && lateSuccess(current, ev.Status, order) {
			// "paid" for an attempt we had closed (replaced by a retry) or
			// seen declined, while the order is still open and unpaid: the
			// money is in for this order — take it.
			apply, conflict = true, false
		}
		if conflict {
			if ev.Status == StatusPaid && isUnpaidFinal(current) {
				// The bank took the money after we gave up on the payment
				// (order cancelled/expired, stock returned) or for a second
				// attempt of an order another attempt already paid: the
				// customer must be refunded.
				if _, err := tx.ExecContext(ctx,
					`UPDATE payments SET raw_webhook = $1, updated_at = now() WHERE id = $2`, []byte(raw), res.PaymentID); err != nil {
					return err
				}
				if err := orders.MarkRefundRequiredTx(ctx, tx, res.OrderID); err != nil {
					return err
				}
				res.RefundRequired = true
				slog.Error("payments: paid callback for an already failed/cancelled payment — order flagged refund_required",
					"provider", providerName, "payment", res.PaymentID, "order", res.OrderID, "current", current)
				return nil
			}
			slog.Error("payments: contradictory callback ignored — needs manual review",
				"provider", providerName, "external_id", ev.ExternalID, "payment", res.PaymentID,
				"current", current, "reported", ev.Status)
			return nil
		}
		if !apply {
			return nil
		}
		if ev.Status == StatusPaid && toTyiyn(ev.Amount) != toTyiyn(amount) {
			slog.Error("payments: paid callback amount mismatch",
				"payment", res.PaymentID, "expected", amount, "reported", ev.Amount)
			return apperr.BadRequest("amount_mismatch", "сумма платежа не совпадает с суммой заказа")
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE payments SET status = $1, raw_webhook = $2, updated_at = now() WHERE id = $3`,
			ev.Status, []byte(raw), res.PaymentID); err != nil {
			return err
		}
		if ev.Status == StatusPaid {
			// A newer attempt may still be open (late success of an
			// earlier one): close it so it can't be paid a second time.
			if _, err := tx.ExecContext(ctx,
				`UPDATE payments SET status = 'cancelled', updated_at = now() WHERE order_id = $1 AND status = 'pending' AND id <> $2`,
				res.OrderID, res.PaymentID); err != nil {
				return err
			}
		}
		var orderStatus orders.OrderStatus
		if err := tx.QueryRowContext(ctx,
			`UPDATE orders SET payment_status = $1, updated_at = now() WHERE id = $2 RETURNING status`,
			ev.Status, res.OrderID).Scan(&orderStatus); err != nil {
			return err
		}
		if ev.Status == StatusPaid && orderStatus == orders.StatusCancelled {
			// The order was cancelled while the customer was paying: the
			// money arrived for goods we no longer hold. Flag it for refund.
			if err := orders.MarkRefundRequiredTx(ctx, tx, res.OrderID); err != nil {
				return err
			}
			res.RefundRequired = true
			slog.Error("payments: payment received for a cancelled order — flagged refund_required",
				"payment", res.PaymentID, "order", res.OrderID)
		}
		if ev.Status == StatusCancelled {
			// The customer cancelled on the bank page: give the stock back.
			// A declined card (failed) keeps the order open for a retry.
			cancelled, err := orders.CancelUnpaidOrderTx(ctx, tx, res.OrderID, "оплата отменена покупателем")
			if err != nil {
				return err
			}
			res.OrderCancelled = cancelled
		}
		res.Status = ev.Status
		res.Applied = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if res.Applied && res.Status == StatusPaid && !res.RefundRequired && s.orders != nil {
		// Now that the money is in, tell staff about the order (cash
		// orders are announced at creation, online ones only here).
		if nerr := s.orders.NotifyOrderPaid(context.WithoutCancel(ctx), res.OrderID); nerr != nil {
			slog.Error("payments: staff notification for paid order failed", "order", res.OrderID, "err", nerr)
		}
	}
	return &res, nil
}

// Payment is a payments row joined with its order number — what the mock
// checkout page needs to render.
type Payment struct {
	ID          string
	OrderID     string
	OrderNumber string
	Provider    string
	ExternalID  string
	Status      Status
	Amount      float64
	Currency    string
}

// GetByExternalID looks a payment up by provider + provider_tx_id.
func (s *Service) GetByExternalID(ctx context.Context, provider, externalID string) (*Payment, error) {
	const q = `
		SELECT p.id, p.order_id, o.order_number, p.provider, p.provider_tx_id, p.status, p.amount, p.currency
		FROM payments p JOIN orders o ON o.id = p.order_id
		WHERE p.provider = $1 AND p.provider_tx_id = $2`
	var p Payment
	err := s.db.QueryRowContext(ctx, q, provider, externalID).Scan(
		&p.ID, &p.OrderID, &p.OrderNumber, &p.Provider, &p.ExternalID, &p.Status, &p.Amount, &p.Currency)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("payment_not_found", "платёж не найден")
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// toTyiyn converts som to tyiyn (1/100) for exact amount comparison.
func toTyiyn(som float64) int64 {
	return int64(math.Round(som * 100))
}
