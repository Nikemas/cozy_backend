// Package staff implements staff (admin panel) authentication and
// role-based access control: phone+password login, server-side sessions,
// and a RequireRole middleware, per §5 of the technical spec.
package staff

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Role mirrors the Postgres staff_role enum (owner/manager/point_staff).
type Role string

const (
	RoleOwner      Role = "owner"
	RoleManager    Role = "manager"
	RolePointStaff Role = "point_staff"
)

// Staff mirrors a row of the staff table.
type Staff struct {
	ID           string
	Phone        string
	PasswordHash string
	Name         string
	Role         Role
	PointID      *string // NULL for owner/manager
	IsActive     bool
	CreatedAt    time.Time
}

// Repo reads staff rows. Password verification and session issuance live
// in Service, not here.
type Repo struct {
	db *sql.DB
}

func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// GetByPhone returns the staff member whose phone matches phone, or nil if
// none exists. phone is compared in canonical form (canonicalPhoneKey) on
// both sides, so "0700 123 456", "996700123456" and "+996700123456" all
// find the same account — including legacy rows stored before staff phones
// were normalized, without a data migration. SQL pre-filters on the last 9
// digits (the staff table is small; the expression is not indexed); the
// exact comparison happens here in Go. An exact textual match wins, then
// the oldest account, so the result is deterministic even if two legacy
// rows spell the same number differently.
func (r *Repo) GetByPhone(ctx context.Context, phone string) (*Staff, error) {
	return getByPhone(ctx, r.db, phone)
}

// queryer is what getByPhone needs: satisfied by both *sql.DB and *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func getByPhone(ctx context.Context, db queryer, phone string) (*Staff, error) {
	const q = `
		SELECT id, phone, password_hash, name, role, point_id, is_active, created_at
		FROM staff
		WHERE phone = $1 OR right(regexp_replace(phone, '\D', '', 'g'), 9) = $2
		ORDER BY (phone = $1) DESC, created_at, id`

	key := canonicalPhoneKey(phone)
	rows, err := db.QueryContext(ctx, q, key, phoneDigitsSuffix(key))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var s Staff
		if err := rows.Scan(&s.ID, &s.Phone, &s.PasswordHash, &s.Name, &s.Role, &s.PointID, &s.IsActive, &s.CreatedAt); err != nil {
			return nil, err
		}
		if s.Phone == key || canonicalPhoneKey(s.Phone) == key {
			return &s, nil
		}
	}
	return nil, rows.Err()
}

// GetByID returns the staff member with the given id, or nil if none
// exists. Used by the session middleware to resolve a session's staff_id.
func (r *Repo) GetByID(ctx context.Context, id string) (*Staff, error) {
	const q = `
		SELECT id, phone, password_hash, name, role, point_id, is_active, created_at
		FROM staff
		WHERE id = $1`
	return r.scanOne(r.db.QueryRowContext(ctx, q, id))
}

func (r *Repo) scanOne(row *sql.Row) (*Staff, error) {
	var s Staff
	err := row.Scan(&s.ID, &s.Phone, &s.PasswordHash, &s.Name, &s.Role, &s.PointID, &s.IsActive, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}
