package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Nikemas/cozy_backend/internal/dbtx"
)

type refreshToken struct {
	ID         string
	CustomerID string
	FamilyID   string
	RotatedAt  *time.Time
}

// refreshStore is what Service needs from refresh_tokens — an interface so
// the rotation/reuse logic can be unit-tested with an in-memory fake.
type refreshStore interface {
	// create starts a new token family (a fresh login).
	create(ctx context.Context, customerID, tokenHash string, expiresAt time.Time) error
	// rotate atomically retires the active token with oldHash and stores
	// its successor (newHash) in the same family. nil means oldHash is not
	// an active token (unknown, expired, revoked or already rotated).
	rotate(ctx context.Context, oldHash, newHash string, expiresAt time.Time) (*refreshToken, error)
	// lookup returns the token with hash in whatever state, or nil.
	lookup(ctx context.Context, tokenHash string) (*refreshToken, error)
	// revokeFamily revokes every still-active token of the family.
	revokeFamily(ctx context.Context, familyID string) error
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

// rotate is a single UPDATE ... RETURNING (so two concurrent refreshes
// with the same token can't both win — the loser sees it as reuse) plus
// the successor's INSERT, in one transaction so a failed insert doesn't
// leave the customer with no valid token.
func (r *refreshRepo) rotate(ctx context.Context, oldHash, newHash string, expiresAt time.Time) (*refreshToken, error) {
	var t *refreshToken
	err := dbtx.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		const upd = `
			UPDATE refresh_tokens SET revoked_at = now(), rotated_at = now()
			WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
			RETURNING id, customer_id, family_id`
		var got refreshToken
		err := tx.QueryRowContext(ctx, upd, oldHash).Scan(&got.ID, &got.CustomerID, &got.FamilyID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}

		const ins = `
			INSERT INTO refresh_tokens (customer_id, token_hash, expires_at, family_id)
			VALUES ($1, $2, $3, $4)`
		if _, err := tx.ExecContext(ctx, ins, got.CustomerID, newHash, expiresAt, got.FamilyID); err != nil {
			return err
		}
		t = &got
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (r *refreshRepo) lookup(ctx context.Context, tokenHash string) (*refreshToken, error) {
	const q = `SELECT id, customer_id, family_id, rotated_at FROM refresh_tokens WHERE token_hash = $1`
	var t refreshToken
	err := r.db.QueryRowContext(ctx, q, tokenHash).Scan(&t.ID, &t.CustomerID, &t.FamilyID, &t.RotatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *refreshRepo) revokeFamily(ctx context.Context, familyID string) error {
	const q = `UPDATE refresh_tokens SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL`
	_, err := r.db.ExecContext(ctx, q, familyID)
	return err
}
