package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type otpCode struct {
	ID    string
	Phone string
	Token string
}

type otpRepo struct {
	db *sql.DB
}

func newOTPRepo(db *sql.DB) *otpRepo {
	return &otpRepo{db: db}
}

func (r *otpRepo) create(ctx context.Context, phone, transactionID, token string, expiresAt time.Time) error {
	const q = `
		INSERT INTO otp_codes (phone, transaction_id, token, expires_at)
		VALUES ($1, $2, $3, $4)`
	_, err := r.db.ExecContext(ctx, q, phone, transactionID, token, expiresAt)
	return err
}

// lastRequestAt returns when the most recent OTP for phone was requested,
// used to enforce the resend cooldown.
func (r *otpRepo) lastRequestAt(ctx context.Context, phone string) (time.Time, bool, error) {
	const q = `SELECT created_at FROM otp_codes WHERE phone = $1 ORDER BY created_at DESC LIMIT 1`
	var t time.Time
	err := r.db.QueryRowContext(ctx, q, phone).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

// countRecentRequests counts OTP requests for phone since the given time,
// used for the hourly rate-limit.
func (r *otpRepo) countRecentRequests(ctx context.Context, phone string, since time.Time) (int, error) {
	const q = `SELECT count(*) FROM otp_codes WHERE phone = $1 AND created_at >= $2`
	var n int
	err := r.db.QueryRowContext(ctx, q, phone, since).Scan(&n)
	return n, err
}

// latestActive returns the most recent unconsumed, unexpired OTP for phone.
func (r *otpRepo) latestActive(ctx context.Context, phone string) (*otpCode, error) {
	const q = `
		SELECT id, phone, token FROM otp_codes
		WHERE phone = $1 AND consumed_at IS NULL AND expires_at > now()
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

func (r *otpRepo) markConsumed(ctx context.Context, id string) error {
	const q = `UPDATE otp_codes SET consumed_at = now() WHERE id = $1`
	_, err := r.db.ExecContext(ctx, q, id)
	return err
}
