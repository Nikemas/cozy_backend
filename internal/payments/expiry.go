package payments

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/Nikemas/cozy_backend/internal/dbtx"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// expiryBatch bounds one ExpirePending pass; the next tick picks up the rest.
const expiryBatch = 100

// DefaultExpiryInterval is how often RunPendingExpiry scans.
const DefaultExpiryInterval = time.Minute

// ExpirePending cancels every online payment that has been pending for
// longer than ttl — the customer abandoned the bank page and no callback
// will come — together with its order, returning the reserved stock. Each
// payment is handled in its own transaction under the same FOR UPDATE lock
// the callback takes, so a callback racing the job is applied exactly once
// either way; a "paid" arriving after expiry flags the order
// refund_required (see HandleCallback). Returns how many were expired.
func (s *Service) ExpirePending(ctx context.Context, ttl time.Duration) (int, error) {
	const q = `
		SELECT id FROM payments
		WHERE status = 'pending' AND created_at < now() - make_interval(secs => $1)
		ORDER BY created_at
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
		ok, err := s.expireOne(ctx, id)
		if err != nil {
			// One bad row must not stall the rest of the batch.
			slog.Error("payments: expiring pending payment failed", "payment", id, "err", err)
			continue
		}
		if ok {
			expired++
		}
	}
	return expired, nil
}

func (s *Service) expireOne(ctx context.Context, paymentID string) (bool, error) {
	var expired bool
	var orderID string
	err := dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		var status Status
		err := tx.QueryRowContext(ctx,
			`SELECT order_id, status FROM payments WHERE id = $1 FOR UPDATE`, paymentID).Scan(&orderID, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if status != StatusPending {
			return nil // settled by a callback in the meantime
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE payments SET status = 'cancelled', updated_at = now() WHERE id = $1`, paymentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE orders SET payment_status = 'cancelled', updated_at = now() WHERE id = $1`, orderID); err != nil {
			return err
		}
		if _, err := orders.CancelUnpaidOrderTx(ctx, tx, orderID, "не оплачен вовремя"); err != nil {
			return err
		}
		expired = true
		return nil
	})
	if err == nil && expired {
		slog.Info("payments: pending payment expired, order cancelled and stock returned", "payment", paymentID, "order", orderID)
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
