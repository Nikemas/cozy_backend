// Package staff implements staff (admin panel) authentication and
// role-based access control: phone+password login, server-side sessions,
// and a RequireRole middleware, per §5 of the technical spec.
package staff

import (
	"context"
	"database/sql"
	"errors"
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
}

// Repo reads staff rows. Password verification and session issuance live
// in Service, not here.
type Repo struct {
	db *sql.DB
}

func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// GetByPhone returns the staff member with the given phone, or nil if none
// exists.
func (r *Repo) GetByPhone(ctx context.Context, phone string) (*Staff, error) {
	const q = `
		SELECT id, phone, password_hash, name, role, point_id, is_active
		FROM staff
		WHERE phone = $1`
	return r.scanOne(r.db.QueryRowContext(ctx, q, phone))
}

// GetByID returns the staff member with the given id, or nil if none
// exists. Used by the session middleware to resolve a session's staff_id.
func (r *Repo) GetByID(ctx context.Context, id string) (*Staff, error) {
	const q = `
		SELECT id, phone, password_hash, name, role, point_id, is_active
		FROM staff
		WHERE id = $1`
	return r.scanOne(r.db.QueryRowContext(ctx, q, id))
}

func (r *Repo) scanOne(row *sql.Row) (*Staff, error) {
	var s Staff
	err := row.Scan(&s.ID, &s.Phone, &s.PasswordHash, &s.Name, &s.Role, &s.PointID, &s.IsActive)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}
