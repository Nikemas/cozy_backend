package staff

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
)

// StaffCreateInput carries the writable fields for creating a new staff
// account. PasswordHash is already bcrypt-hashed by Service.CreateStaff —
// Repo never sees a plaintext password.
type StaffCreateInput struct {
	Phone        string
	PasswordHash string
	Name         string
	Role         Role
	PointID      *string
}

// StaffUpdateInput carries the writable fields for updating an existing
// staff account. PasswordHash is nil to leave the password unchanged.
type StaffUpdateInput struct {
	Name         string
	Role         Role
	PointID      *string
	IsActive     bool
	PasswordHash *string
}

// List returns every staff account, most recently created first. Admin-only
// (gated by RequireRole(RoleOwner) at the route level), so unlike
// GetByPhone/GetByID this intentionally includes inactive accounts too.
func (r *Repo) List(ctx context.Context) ([]Staff, error) {
	const q = `
		SELECT id, phone, password_hash, name, role, point_id, is_active, created_at
		FROM staff
		ORDER BY created_at DESC`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	list := []Staff{}
	for rows.Next() {
		var s Staff
		if err := rows.Scan(&s.ID, &s.Phone, &s.PasswordHash, &s.Name, &s.Role, &s.PointID, &s.IsActive, &s.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return list, nil
}

// Create inserts a new, active staff account and returns the row as stored.
// Translates a duplicate phone (SQLSTATE 23505) into apperr.Conflict and an
// unknown point_id (23503) into apperr.BadRequest instead of a raw 500 — see
// translateStaffWriteErr.
//
// The UNIQUE constraint only catches the exact same spelling, so a legacy
// row holding the same number in another format (e.g. "0700123456" vs
// "+996700123456") is looked up first and reported as phone_taken too.
func (r *Repo) Create(ctx context.Context, in StaffCreateInput) (*Staff, error) {
	existing, err := r.GetByPhone(ctx, in.Phone)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, apperr.Conflict("phone_taken", "этот номер телефона уже используется")
	}

	if err := checkPointKind(ctx, r.db, in.Role, in.PointID); err != nil {
		return nil, err
	}

	const q = `
		INSERT INTO staff (phone, password_hash, name, role, point_id, is_active)
		VALUES ($1, $2, $3, $4, $5, true)
		RETURNING id, phone, password_hash, name, role, point_id, is_active, created_at`

	var s Staff
	err = r.db.QueryRowContext(ctx, q, in.Phone, in.PasswordHash, in.Name, in.Role, in.PointID).
		Scan(&s.ID, &s.Phone, &s.PasswordHash, &s.Name, &s.Role, &s.PointID, &s.IsActive, &s.CreatedAt)
	if err != nil {
		return nil, translateStaffWriteErr(err)
	}
	return &s, nil
}

// Update applies in to the staff account with the given id, enforcing the
// "there must always be at least one active owner" invariant atomically: an
// update that would demote (role != owner) or deactivate (is_active =
// false) the *last* active owner is rejected with
// apperr.Conflict("last_owner", ...) and leaves the row unchanged.
//
// The whole operation — locking the target row, locking and counting the
// other active owners, and the write itself — runs inside one transaction
// to close the race window between "check" and "write": without it, two
// concurrent requests could each see (an now-stale) "another active owner
// exists" and both succeed, leaving zero.
func (r *Repo) Update(ctx context.Context, id string, in StaffUpdateInput) (*Staff, error) {
	var updated Staff
	err := dbtx.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		const getQ = `
			SELECT id, phone, password_hash, name, role, point_id, is_active, created_at
			FROM staff
			WHERE id = $1
			FOR UPDATE`

		var current Staff
		err := tx.QueryRowContext(ctx, getQ, id).
			Scan(&current.ID, &current.Phone, &current.PasswordHash, &current.Name, &current.Role,
				&current.PointID, &current.IsActive, &current.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFound("staff_not_found", "сотрудник не найден")
		}
		if err != nil {
			return err
		}

		losesOwnerStatus := current.Role == RoleOwner && current.IsActive && (in.Role != RoleOwner || !in.IsActive)
		if losesOwnerStatus {
			others, err := countOtherActiveOwners(ctx, tx, id)
			if err != nil {
				return err
			}
			if others == 0 {
				return apperr.Conflict("last_owner", "нельзя понизить или деактивировать последнего владельца")
			}
		}

		// Only a changed role/point is checked, so toggling or renaming a
		// legacy account (e.g. point_staff at a point later turned into a
		// warehouse) keeps working.
		if in.Role != current.Role || !samePointID(in.PointID, current.PointID) {
			if err := checkPointKind(ctx, tx, in.Role, in.PointID); err != nil {
				return err
			}
		}

		passwordHash := current.PasswordHash
		if in.PasswordHash != nil {
			passwordHash = *in.PasswordHash
		}

		const updateQ = `
			UPDATE staff
			SET name = $2, role = $3, point_id = $4, is_active = $5, password_hash = $6
			WHERE id = $1
			RETURNING id, phone, password_hash, name, role, point_id, is_active, created_at`

		err = tx.QueryRowContext(ctx, updateQ, id, in.Name, in.Role, in.PointID, in.IsActive, passwordHash).
			Scan(&updated.ID, &updated.Phone, &updated.PasswordHash, &updated.Name, &updated.Role,
				&updated.PointID, &updated.IsActive, &updated.CreatedAt)
		if err != nil {
			return translateStaffWriteErr(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// SetPassword replaces the password hash of one staff account.
func (r *Repo) SetPassword(ctx context.Context, id, passwordHash string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE staff SET password_hash = $2 WHERE id = $1`, id, passwordHash)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("staff_not_found", "сотрудник не найден")
	}
	return nil
}

// countOtherActiveOwners returns how many staff rows other than excludeID
// are active owners, as part of tx. It locks those rows (SELECT ... FOR
// UPDATE) so a concurrent transaction can't modify them — and so change the
// answer — before this transaction commits or rolls back. Equivalent to a
// `SELECT COUNT(*) ... FOR UPDATE`, which Postgres itself rejects (FOR
// UPDATE isn't allowed together with an aggregate function).
func countOtherActiveOwners(ctx context.Context, tx *sql.Tx, excludeID string) (int, error) {
	const q = `
		SELECT id FROM staff
		WHERE role = 'owner' AND is_active = true AND id != $1
		FOR UPDATE`

	rows, err := tx.QueryContext(ctx, q, excludeID)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

// translateStaffWriteErr maps Postgres constraint violations from
// Create/Update into apperr responses instead of a raw 500: phone has a
// UNIQUE constraint, and point_id a foreign key into points_of_sale.
func translateStaffWriteErr(err error) error {
	switch pgErrCode(err) {
	case pgUniqueViolation:
		return apperr.Conflict("phone_taken", "этот номер телефона уже используется")
	case pgForeignKeyViolation:
		return apperr.BadRequest("invalid_point_id", "точка продаж не найдена")
	}
	return err
}

// rowQueryer is satisfied by both *sql.DB and *sql.Tx.
type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// checkPointKind enforces that a pinned role's point is of the right kind:
// point_staff works at a branch, a storekeeper at a warehouse
// (Role.RequiredPointKind). An unknown point is left to the foreign key
// (translateStaffWriteErr → invalid_point_id).
func checkPointKind(ctx context.Context, db rowQueryer, role Role, pointID *string) error {
	want := role.RequiredPointKind()
	if want == "" || pointID == nil {
		return nil
	}
	var kind string
	err := db.QueryRowContext(ctx, `SELECT kind FROM points_of_sale WHERE id::text = $1`, *pointID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.BadRequest("invalid_point_id", "точка продаж не найдена")
	}
	if err != nil {
		return err
	}
	if kind == want {
		return nil
	}
	if role == RoleStorekeeper {
		return apperr.BadRequest("storekeeper_needs_warehouse", "кладовщика можно привязать только к складу")
	}
	return apperr.BadRequest("point_staff_needs_branch", "сотрудника точки можно привязать только к филиалу")
}

func samePointID(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
