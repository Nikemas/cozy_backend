//go:build integration

package integration

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// uniqueKGPhone returns a random valid local-format number (0XXXXXXXXX) and
// its canonical +996 form, so parallel tests never collide on staff.phone.
func uniqueKGPhone() (local, canonical string) {
	sub := fmt.Sprintf("7%08d", rand.IntN(100_000_000))
	return "0" + sub, "+996" + sub
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want apperr code %q", err, code)
	}
}

func TestEnsureOwnerCreatesThenResets(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	local, canonical := uniqueKGPhone()

	// First run: creates an owner, phone stored canonical.
	res, err := staff.EnsureOwner(ctx, testDB, staff.EnsureOwnerInput{Phone: local, Password: "first-pass-1"})
	if err != nil {
		t.Fatalf("EnsureOwner (create): %v", err)
	}
	if !res.Created || res.Phone != canonical {
		t.Fatalf("result = %+v, want created with phone %s", res, canonical)
	}
	var role, name string
	var active bool
	if err := testDB.QueryRowContext(ctx, `SELECT role, name, is_active FROM staff WHERE id = $1`, res.StaffID).
		Scan(&role, &name, &active); err != nil {
		t.Fatal(err)
	}
	if role != "owner" || name != staff.DefaultOwnerName || !active {
		t.Fatalf("row = role %s name %q active %v", role, name, active)
	}

	// The hash is what Login expects: log in with another spelling.
	svc := staff.NewService(testDB)
	if _, err := svc.Login(ctx, canonical, "first-pass-1"); err != nil {
		t.Fatalf("Login after create: %v", err)
	}

	// Deactivate it, then a second run resets the password, re-activates
	// and revokes the open session.
	if _, err := testDB.ExecContext(ctx, `UPDATE staff SET is_active = false WHERE id = $1`, res.StaffID); err != nil {
		t.Fatal(err)
	}
	res2, err := staff.EnsureOwner(ctx, testDB, staff.EnsureOwnerInput{Phone: "+996 " + canonical[4:], Password: "second-pass-2", Name: "ignored"})
	if err != nil {
		t.Fatalf("EnsureOwner (reset): %v", err)
	}
	if res2.Created || res2.StaffID != res.StaffID {
		t.Fatalf("reset result = %+v, want same account %s", res2, res.StaffID)
	}
	var open int
	if err := testDB.QueryRowContext(ctx,
		`SELECT count(*) FROM staff_sessions WHERE staff_id = $1 AND revoked_at IS NULL`, res.StaffID).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatalf("%d sessions still open after reset", open)
	}
	if _, err := svc.Login(ctx, local, "first-pass-1"); err == nil {
		t.Fatal("old password still works after reset")
	}
	if _, err := svc.Login(ctx, local, "second-pass-2"); err != nil {
		t.Fatalf("Login with the new password: %v", err)
	}
}

func TestEnsureOwnerRefusesNonOwnerAndBadInput(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	local, canonical := uniqueKGPhone()
	if _, err := testDB.ExecContext(ctx,
		`INSERT INTO staff (phone, password_hash, name, role) VALUES ($1, 'x', 'Manager', 'manager')`, canonical); err != nil {
		t.Fatal(err)
	}

	_, err := staff.EnsureOwner(ctx, testDB, staff.EnsureOwnerInput{Phone: local, Password: "long-enough-1"})
	assertCode(t, err, "not_owner")

	_, err = staff.EnsureOwner(ctx, testDB, staff.EnsureOwnerInput{Phone: "12345", Password: "long-enough-1"})
	assertCode(t, err, "invalid_phone")

	_, err = staff.EnsureOwner(ctx, testDB, staff.EnsureOwnerInput{Phone: local, Password: "short"})
	assertCode(t, err, "invalid_password")
}

func TestStaffLoginToleratesLegacyStoredPhone(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	local, canonical := uniqueKGPhone()
	// A row created before normalization: stored with spaces, local format.
	legacy := local[:4] + " " + local[4:7] + " " + local[7:]
	hash := "$2a$10$UnWYtHLgrH/2tNOF3VF.k.Mno4/aqJ1u8/dKRjqdLBiwUsDvBTD7W" // not this password
	var id string
	if err := testDB.QueryRowContext(ctx,
		`INSERT INTO staff (phone, password_hash, name, role) VALUES ($1, $2, 'Legacy', 'owner') RETURNING id`,
		legacy, hash).Scan(&id); err != nil {
		t.Fatal(err)
	}

	repo := staff.NewRepo(testDB)
	for _, typed := range []string{canonical, local, canonical[1:], legacy} {
		st, err := repo.GetByPhone(ctx, typed)
		if err != nil || st == nil || st.ID != id {
			t.Fatalf("GetByPhone(%q) = %+v, %v; want the legacy row %s", typed, st, err, id)
		}
	}

	// Creating another account with the same number in canonical form is
	// a conflict, not a second account.
	svc := staff.NewService(testDB)
	_, err := svc.CreateStaff(ctx, staff.CreateStaffInput{Phone: canonical, Password: "long-enough-1", Name: "Dup", Role: staff.RoleManager})
	assertCode(t, err, "phone_taken")

	// EnsureOwner resets the legacy owner instead of creating a duplicate,
	// and login then works with any spelling.
	res, err := staff.EnsureOwner(ctx, testDB, staff.EnsureOwnerInput{Phone: canonical, Password: "new-owner-pass"})
	if err != nil || res.Created || res.StaffID != id {
		t.Fatalf("EnsureOwner on legacy row = %+v, %v", res, err)
	}
	if _, err := svc.Login(ctx, "+996 "+canonical[4:], "new-owner-pass"); err != nil {
		t.Fatalf("Login against legacy row: %v", err)
	}
}
