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
	CreateOnlineOrder(ctx context.Context, customerID string, items []orders.OrderItemInput, addressID, pickupPointID *string, provider string) (*orders.Order, string, error)
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
//   - failed / cancelled (or the provider couldn't even open a session):
//     the order is cancelled and its stock returned, once, in the same
//     transaction as the payment update (orders.CancelUnpaidOrderTx) —
//     only if the order is still 'placed'. The customer places a new order
//     to retry; there is no "retry payment on the same order" yet.
//   - Duplicate callbacks are no-ops (payment row locked FOR UPDATE, same
//     status ⇒ nothing applied), so stock is never returned twice.
//   - Abandoned payments (no callback ever) keep their reservation until
//     staff cancel the order — no expiry job yet (see tasks/todo.md).
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
func (s *Service) PlaceOnlineOrder(ctx context.Context, customerID string, items []orders.OrderItemInput, addressID, pickupPointID *string) (*orders.Order, string, error) {
	if err := s.provider.Ready(); err != nil {
		return nil, "", err
	}

	order, paymentID, err := s.orders.CreateOnlineOrder(ctx, customerID, items, addressID, pickupPointID, s.provider.Name())
	if err != nil {
		return nil, "", err
	}

	sess, err := s.provider.CreatePayment(ctx, CreateRequest{
		PaymentID:   paymentID,
		OrderID:     order.ID,
		OrderNumber: order.OrderNumber,
		Amount:      order.TotalAmount,
		Currency:    Currency,
		ReturnURL:   s.publicBaseURL + "/order/" + order.OrderNumber + "/done",
	})
	if err == nil {
		const q = `UPDATE payments SET provider_tx_id = $1, updated_at = now() WHERE id = $2`
		_, err = s.db.ExecContext(ctx, q, sess.ExternalID, paymentID)
	}
	if err != nil {
		slog.Error("payments: opening checkout session failed, cancelling order",
			"provider", s.provider.Name(), "order", order.OrderNumber, "err", err)
		// Detached context: the compensation must run even if the client
		// has already gone away.
		if abortErr := s.abort(context.WithoutCancel(ctx), paymentID, order.ID); abortErr != nil {
			slog.Error("payments: compensating order cancel failed — stock stays reserved",
				"order", order.OrderNumber, "err", abortErr)
		}
		var appErr *apperr.AppError
		if errors.As(err, &appErr) {
			return nil, "", err
		}
		return nil, "", apperr.New(http.StatusBadGateway, "payment_create_failed", "не удалось создать платёж, попробуйте ещё раз")
	}
	return order, sess.RedirectURL, nil
}

// abort marks a still-pending payment failed and cancels its order,
// returning stock.
func (s *Service) abort(ctx context.Context, paymentID, orderID string) error {
	return dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE payments SET status = 'failed', updated_at = now() WHERE id = $1 AND status = 'pending'`, paymentID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // a callback already settled it
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE orders SET payment_status = 'failed', updated_at = now() WHERE id = $1`, orderID); err != nil {
			return err
		}
		_, err = orders.CancelUnpaidOrderTx(ctx, tx, orderID)
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
		var current Status
		var amount float64
		const lockQ = `
			SELECT id, order_id, status, amount FROM payments
			WHERE provider = $1 AND provider_tx_id = $2
			FOR UPDATE`
		err := tx.QueryRowContext(ctx, lockQ, providerName, ev.ExternalID).Scan(&res.PaymentID, &res.OrderID, &current, &amount)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFound("payment_not_found", "платёж не найден")
		}
		if err != nil {
			return err
		}
		res.Status = current

		apply, conflict := transition(current, ev.Status)
		if conflict {
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
		var orderStatus orders.OrderStatus
		if err := tx.QueryRowContext(ctx,
			`UPDATE orders SET payment_status = $1, updated_at = now() WHERE id = $2 RETURNING status`,
			ev.Status, res.OrderID).Scan(&orderStatus); err != nil {
			return err
		}
		if ev.Status == StatusPaid && orderStatus == orders.StatusCancelled {
			// Staff cancelled the order while the customer was paying: the
			// money arrived for goods we no longer hold. Record it, flag it.
			slog.Error("payments: payment received for a cancelled order — refund needed",
				"payment", res.PaymentID, "order", res.OrderID)
		}
		if ev.Status == StatusFailed || ev.Status == StatusCancelled {
			cancelled, err := orders.CancelUnpaidOrderTx(ctx, tx, res.OrderID)
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
