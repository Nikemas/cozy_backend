package payments

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// expiryBatch bounds one ExpirePending pass; the next tick picks up the rest.
const expiryBatch = 100

// DefaultExpiryInterval is how often RunPendingExpiry scans.
const DefaultExpiryInterval = time.Minute

// unpaidExpiredCond selects open online orders ($1 = TTL seconds) whose
// latest payment attempt was abandoned: still pending TTL after it was
// opened, or failed TTL ago and never retried. An order with no payments
// row at all (can't normally happen) ages from its own created_at.
const unpaidExpiredCond = `
	o.status = 'placed' AND o.payment_method = 'online_card' AND o.payment_status IN ('pending', 'failed')
	AND COALESCE(
		(SELECT CASE WHEN p.status = 'pending' THEN p.created_at ELSE p.updated_at END
		 FROM payments p WHERE p.order_id = o.id
		 ORDER BY p.created_at DESC, p.id DESC LIMIT 1),
		o.created_at) < now() - make_interval(secs => $1)`

// ExpirePending cancels every online order whose latest payment attempt
// was abandoned for longer than ttl — the customer left the bank page (no
// callback will come) or a declined payment was never retried — returning
// the reserved stock. Only the latest attempt counts, so a retry restarts
// the clock. Each order is handled in its own transaction under the same
// order-row lock callbacks and retries take, so a racing callback or retry
// is applied exactly once either way; a "paid" arriving after expiry flags
// the order refund_required (see HandleCallback). Returns how many orders
// were expired.
func (s *Service) ExpirePending(ctx context.Context, ttl time.Duration) (int, error) {
	q := `SELECT o.id FROM orders o WHERE` + unpaidExpiredCond + `
		ORDER BY o.created_at
		LIMIT $2`
	rows, err := s.db.QueryContext(ctx, q, ttl.Seconds(), expiryBatch)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()

	expired := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			return expired, ctx.Err()
		}
		ok, err := s.expireOne(ctx, id, ttl)
		if err != nil {
			// One bad row must not stall the rest of the batch.
			slog.Error("payments: expiring unpaid order failed", "order", id, "err", err)
			continue
		}
		if ok {
			expired++
		}
	}
	return expired, nil
}

// expireOne re-checks orderID under its row lock (a callback or retry may
// have got there first) and cancels it: a still-pending attempt is closed
// and the stock returned (orders.CancelUnpaidOrderTx).
func (s *Service) expireOne(ctx context.Context, orderID string, ttl time.Duration) (bool, error) {
	var expired bool
	err := dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := orders.LockOrderTx(ctx, tx, orderID); err != nil {
			var appErr *apperr.AppError
			if errors.As(err, &appErr) && appErr.Code == "order_not_found" {
				return nil
			}
			return err
		}
		var still bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM orders o WHERE`+unpaidExpiredCond+` AND o.id = $2)`,
			ttl.Seconds(), orderID).Scan(&still); err != nil {
			return err
		}
		if !still {
			return nil // paid, retried or cancelled in the meantime
		}
		ok, err := orders.CancelUnpaidOrderTx(ctx, tx, orderID, "не оплачен вовремя")
		if err != nil {
			return err
		}
		expired = ok
		return nil
	})
	if err == nil && expired {
		slog.Info("payments: unpaid online order expired, order cancelled and stock returned", "order", orderID)
	}
	return expired, err
}

// RunPendingExpiry runs ExpirePending every interval (and once right
// away) until ctx is cancelled. The returned channel is closed when the
// loop has exited, so shutdown can wait for an in-flight pass to finish.
func RunPendingExpiry(ctx context.Context, svc *Service, ttl, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	if interval <= 0 {
		interval = DefaultExpiryInterval
	}
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("payments: expiry job panicked, stopped", "panic", r)
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if n, err := svc.ExpirePending(ctx, ttl); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("payments: expiry pass failed", "err", err)
			} else if n > 0 {
				slog.Info("payments: expired pending payments", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}
