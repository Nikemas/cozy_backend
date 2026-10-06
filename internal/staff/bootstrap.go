package staff

import (
	"context"
	"database/sql"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
)

// DefaultOwnerName is the display name EnsureOwner gives a newly created
// owner when none is passed; it can be changed later in the admin panel.
const DefaultOwnerName = "Владелец"

// EnsureOwnerInput is the input of EnsureOwner. Password is plaintext; it is
// validated and bcrypt-hashed exactly like the admin panel does
// (validatePassword + hashPassword) and never stored or logged as is.
type EnsureOwnerInput struct {
	Phone    string
	Password string
	Name     string // used only when the account is created; blank = DefaultOwnerName
}

// EnsureOwnerResult reports what EnsureOwner did.
type EnsureOwnerResult struct {
	StaffID string
	Phone   string // stored phone of the account
	Created bool   // false = an existing owner's password was reset
}

// EnsureOwner is the server-side bootstrap for the first owner account
// (cmd/server "create-owner") and the recovery path when the only owner has
// lost their password. Idempotent:
//
//   - no staff account with this phone (any spelling, see GetByPhone):
//     creates an active owner;
//   - an owner with this phone exists: sets the new password, re-activates
//     the account and revokes all its sessions;
//   - a manager/point_staff account has this phone: refused with
//     apperr.Conflict("not_owner") — promoting staff is an owner action in
//     the admin panel, not something a shell command should do silently.
//
// Everything runs in one transaction.
func EnsureOwner(ctx context.Context, db *sql.DB, in EnsureOwnerInput) (EnsureOwnerResult, error) {
	phone, err := normalizeStaffPhone(in.Phone)
	if err != nil {
		return EnsureOwnerResult{}, err
	}
	if err := validatePassword(in.Password); err != nil {
		return EnsureOwnerResult{}, err
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		return EnsureOwnerResult{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = DefaultOwnerName
	}

	var res EnsureOwnerResult
	err = dbtx.WithTx(ctx, db, func(tx *sql.Tx) error {
		existing, err := getByPhone(ctx, tx, phone)
		if err != nil {
			return err
		}
		if existing == nil {
			res, err = insertOwner(ctx, tx, phone, hash, name)
			return err
		}
		if existing.Role != RoleOwner {
			return apperr.Conflict("not_owner",
				"с этим телефоном уже есть сотрудник с ролью "+string(existing.Role)+"; используйте другой номер или смените роль в админке")
		}
		res = EnsureOwnerResult{StaffID: existing.ID, Phone: existing.Phone}
		return resetOwner(ctx, tx, existing.ID, hash)
	})
	if err != nil {
		return EnsureOwnerResult{}, err
	}
	return res, nil
}

func insertOwner(ctx context.Context, tx *sql.Tx, phone, hash, name string) (EnsureOwnerResult, error) {
	const q = `
		INSERT INTO staff (phone, password_hash, name, role, is_active)
		VALUES ($1, $2, $3, 'owner', true)
		RETURNING id`
	var id string
	if err := tx.QueryRowContext(ctx, q, phone, hash, name).Scan(&id); err != nil {
		return EnsureOwnerResult{}, translateStaffWriteErr(err)
	}
	return EnsureOwnerResult{StaffID: id, Phone: phone, Created: true}, nil
}

func resetOwner(ctx context.Context, tx *sql.Tx, id, hash string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE staff SET password_hash = $2, is_active = true WHERE id = $1`, id, hash); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE staff_sessions SET revoked_at = now() WHERE staff_id = $1 AND revoked_at IS NULL`, id)
	return err
}
