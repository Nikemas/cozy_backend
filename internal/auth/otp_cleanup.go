package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"
)

// DefaultOTPCleanupInterval is how often RunOTPCleanup purges old
// otp_codes rows. Retention is measured in days, so a few passes a day
// keep the table within a fraction of a day of the configured limit.
const DefaultOTPCleanupInterval = 6 * time.Hour

// deleteOTPCodesBefore removes otp_codes rows created before cutoff and
// returns how many were deleted. No index on created_at is needed: the
// table only ever holds the retention window's worth of rows and this runs
// a few times a day.
func deleteOTPCodesBefore(ctx context.Context, db *sql.DB, cutoff time.Time) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM otp_codes WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RunOTPCleanup deletes otp_codes rows older than retention every interval
// (and once right away) until ctx is cancelled. The returned channel is
// closed when the loop has exited, so shutdown can wait for an in-flight
// pass.
func RunOTPCleanup(ctx context.Context, db *sql.DB, retention, interval time.Duration) <-chan struct{} {
	return runOTPCleanup(ctx, db, retention, interval, time.Now)
}

// runOTPCleanup is RunOTPCleanup with an injectable clock for tests.
func runOTPCleanup(ctx context.Context, db *sql.DB, retention, interval time.Duration, now func() time.Time) <-chan struct{} {
	done := make(chan struct{})
	if interval <= 0 {
		interval = DefaultOTPCleanupInterval
	}
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("auth: otp cleanup panicked, stopped", "panic", r)
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			n, err := deleteOTPCodesBefore(ctx, db, now().Add(-retention))
			switch {
			case err != nil && !errors.Is(err, context.Canceled):
				slog.Error("auth: otp cleanup pass failed", "err", err)
			case n > 0:
				slog.Info("auth: old otp codes deleted", "count", n)
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
