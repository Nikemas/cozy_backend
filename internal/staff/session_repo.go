package staff

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Session mirrors a row of the staff_sessions table.
type Session struct {
	ID        string
	StaffID   string
	ExpiresAt time.Time
	RevokedAt *time.Time
}

type sessionRepo struct {
	db *sql.DB
}

func newSessionRepo(db *sql.DB) *sessionRepo {
	return &sessionRepo{db: db}
}

func (r *sessionRepo) create(ctx context.Context, staffID, tokenHash string, expiresAt time.Time) error {
	const q = `
		INSERT INTO staff_sessions (staff_id, token_hash, expires_at)
		VALUES ($1, $2, $3)`
	_, err := r.db.ExecContext(ctx, q, staffID, tokenHash, expiresAt)
	return err
}

// getActiveByHash returns the session row only if it exists, isn't revoked
// and hasn't expired — anything else is treated as an invalid session by
// the caller.
func (r *sessionRepo) getActiveByHash(ctx context.Context, tokenHash string) (*Session, error) {
	const q = `
		SELECT id, staff_id, expires_at, revoked_at FROM staff_sessions
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()`

	var s Session
	err := r.db.QueryRowContext(ctx, q, tokenHash).Scan(&s.ID, &s.StaffID, &s.ExpiresAt, &s.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// revokeByHash revokes the session with the given token hash, if any.
// Revoking an already-revoked or nonexistent session is a no-op, so logout
// stays idempotent.
func (r *sessionRepo) revokeByHash(ctx context.Context, tokenHash string) error {
	const q = `UPDATE staff_sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`
	_, err := r.db.ExecContext(ctx, q, tokenHash)
	return err
}

// revokeAllForStaff revokes every open session of one staff account
// (password reset, deactivation).
func (r *sessionRepo) revokeAllForStaff(ctx context.Context, staffID string) error {
	const q = `UPDATE staff_sessions SET revoked_at = now() WHERE staff_id = $1 AND revoked_at IS NULL`
	_, err := r.db.ExecContext(ctx, q, staffID)
	return err
}
