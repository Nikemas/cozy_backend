package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type refreshToken struct {
	ID         string
	CustomerID string
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

type refreshRepo struct {
	db *sql.DB
}

func newRefreshRepo(db *sql.DB) *refreshRepo {
	return &refreshRepo{db: db}
}

func (r *refreshRepo) create(ctx context.Context, customerID, tokenHash string, expiresAt time.Time) error {
	const q = `
		INSERT INTO refresh_tokens (customer_id, token_hash, expires_at)
		VALUES ($1, $2, $3)`
	_, err := r.db.ExecContext(ctx, q, customerID, tokenHash, expiresAt)
	return err
}

// getActiveByHash returns the token row only if it exists, isn't revoked
// and hasn't expired — anything else is treated as an invalid refresh
// token by the caller.
func (r *refreshRepo) getActiveByHash(ctx context.Context, tokenHash string) (*refreshToken, error) {
	const q = `
		SELECT id, customer_id, expires_at, revoked_at FROM refresh_tokens
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()`

	var t refreshToken
	err := r.db.QueryRowContext(ctx, q, tokenHash).Scan(&t.ID, &t.CustomerID, &t.ExpiresAt, &t.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *refreshRepo) revoke(ctx context.Context, id string) error {
	const q = `UPDATE refresh_tokens SET revoked_at = now() WHERE id = $1`
	_, err := r.db.ExecContext(ctx, q, id)
	return err
}
