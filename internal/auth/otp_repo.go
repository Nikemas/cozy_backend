package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
)

type otpCode struct {
	ID    string
	Phone string
	Token string
}

// otpSendLimits are the checks reserve runs before admitting a new SMS.
// Zero disables the IP and global checks.
type otpSendLimits struct {
	Cooldown  time.Duration // same phone, between two requests
	PerPhone  int           // same phone, rolling hour
	PerIP     int           // same client IP, rolling hour
	GlobalDay int           // all phones, rolling 24h
}

// otpStore is what Service needs from the otp_codes table — an interface
// so the service flow can be unit-tested with an in-memory fake.
type otpStore interface {
	reserve(ctx context.Context, phone, ip, transactionID string, expiresAt time.Time, lim otpSendLimits) (id string, err error)
	setToken(ctx context.Context, id, token string) error
	burn(ctx context.Context, id string) error
	latestActive(ctx context.Context, phone string) (*otpCode, error)
	takeAttempt(ctx context.Context, id string, maxAttempts int) (attempt int, ok bool, err error)
	consume(ctx context.Context, id string) (bool, error)
}

type otpRepo struct {
	db *sql.DB
}

func newOTPRepo(db *sql.DB) *otpRepo {
	return &otpRepo{db: db}
}

// reserve checks every send limit and, if they all pass, inserts the OTP
// row (with an empty provider token — setToken fills it once the SMS is
// out) in one transaction under a per-phone advisory lock. The lock makes
// the per-phone cooldown/hourly checks race-free: two concurrent requests
// for one phone can no longer both see "no recent code" and both send.
// Reserving before the (slow, external) SMS call keeps the lock off the
// network round-trip, and means a failed send still counts toward limits.
func (r *otpRepo) reserve(ctx context.Context, phone, ip, transactionID string, expiresAt time.Time, lim otpSendLimits) (string, error) {
	var id string
	err := dbtx.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('otp:' || $1, 0))`, phone); err != nil {
			return err
		}

		var lastAt sql.NullTime
		var phoneHour int
		const phoneQ = `
			SELECT max(created_at), count(*) FILTER (WHERE created_at >= now() - interval '1 hour')
			FROM otp_codes WHERE phone = $1`
		if err := tx.QueryRowContext(ctx, phoneQ, phone).Scan(&lastAt, &phoneHour); err != nil {
			return err
		}
		if lastAt.Valid && time.Since(lastAt.Time) < lim.Cooldown {
			return apperr.New(http.StatusTooManyRequests, "otp_cooldown", "код уже отправлен, попробуйте чуть позже")
		}
		if phoneHour >= lim.PerPhone {
			return apperr.New(http.StatusTooManyRequests, "otp_rate_limited", "слишком много запросов кода, попробуйте позже")
		}

		if lim.PerIP > 0 && ip != "" {
			var n int
			const ipQ = `SELECT count(*) FROM otp_codes WHERE request_ip = $1 AND created_at >= now() - interval '1 hour'`
			if err := tx.QueryRowContext(ctx, ipQ, ip).Scan(&n); err != nil {
				return err
			}
			if n >= lim.PerIP {
				return apperr.New(http.StatusTooManyRequests, "otp_rate_limited", "слишком много запросов кода, попробуйте позже")
			}
		}

		if lim.GlobalDay > 0 {
			var n int
			const dayQ = `SELECT count(*) FROM otp_codes WHERE created_at >= now() - interval '24 hours'`
			if err := tx.QueryRowContext(ctx, dayQ).Scan(&n); err != nil {
				return err
			}
			if n >= lim.GlobalDay {
				slog.ErrorContext(ctx, "auth: global daily OTP limit reached — all SMS logins are refused until the window rolls over (OTP_MAX_PER_DAY)", "limit", lim.GlobalDay)
				return apperr.New(http.StatusTooManyRequests, "otp_daily_limit", "вход по SMS временно недоступен, попробуйте позже")
			}
		}

		var ipArg any
		if ip != "" {
			ipArg = ip
		}
		const insQ = `
			INSERT INTO otp_codes (phone, transaction_id, token, expires_at, request_ip)
			VALUES ($1, $2, '', $3, $4)
			RETURNING id`
		return tx.QueryRowContext(ctx, insQ, phone, transactionID, expiresAt, ipArg).Scan(&id)
	})
	return id, err
}

func (r *otpRepo) setToken(ctx context.Context, id, token string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE otp_codes SET token = $2 WHERE id = $1`, id, token)
	return err
}

// burn makes a code unusable (failed send, or too many wrong guesses)
// while keeping the row so it still counts toward the send limits.
func (r *otpRepo) burn(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE otp_codes SET consumed_at = now() WHERE id = $1 AND consumed_at IS NULL`, id)
	return err
}

// latestActive returns the most recent unconsumed, unexpired OTP for phone
// whose SMS actually went out (token set).
func (r *otpRepo) latestActive(ctx context.Context, phone string) (*otpCode, error) {
	const q = `
		SELECT id, phone, token FROM otp_codes
		WHERE phone = $1 AND consumed_at IS NULL AND expires_at > now() AND token <> ''
		ORDER BY created_at DESC LIMIT 1`

	var c otpCode
	err := r.db.QueryRowContext(ctx, q, phone).Scan(&c.ID, &c.Phone, &c.Token)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// takeAttempt atomically spends one verification attempt on the code,
// BEFORE the code is checked with the provider — so parallel guesses
// can't exceed maxAttempts between a check and an increment. ok=false
// means the budget is already spent (or the code was consumed meanwhile).
func (r *otpRepo) takeAttempt(ctx context.Context, id string, maxAttempts int) (int, bool, error) {
	const q = `
		UPDATE otp_codes SET verify_attempts = verify_attempts + 1
		WHERE id = $1 AND consumed_at IS NULL AND verify_attempts < $2
		RETURNING verify_attempts`
	var n int
	err := r.db.QueryRowContext(ctx, q, id, maxAttempts).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return n, true, nil
}

// consume marks a correctly verified code used. false means another
// request consumed it first — the code must not log in twice.
func (r *otpRepo) consume(ctx context.Context, id string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE otp_codes SET consumed_at = now() WHERE id = $1 AND consumed_at IS NULL`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
