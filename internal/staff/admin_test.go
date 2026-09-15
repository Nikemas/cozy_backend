package staff

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

func newTestAdminService(admin *fakeStaffAdmin) *Service {
	return &Service{admin: admin}
}

func strPtr(s string) *string { return &s }

func assertAppErrCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected *apperr.AppError, got %T: %v", err, err)
	}
	if appErr.Code != wantCode {
		t.Fatalf("expected code %q, got %q (%v)", wantCode, appErr.Code, appErr)
	}
}

// --- last-owner invariant ---

func TestUpdateStaffLastOwnerRejectsDemote(t *testing.T) {
	owner := &Staff{ID: "s1", Phone: "+996700000001", Name: "Owner", Role: RoleOwner, IsActive: true}
	admin := newFakeStaffAdmin(owner)
	svc := newTestAdminService(admin)

	_, err := svc.UpdateStaff(context.Background(), "s1", UpdateStaffInput{
		Name: "Owner", Role: RoleManager, PointID: nil, IsActive: true,
	})
	assertAppErrCode(t, err, "last_owner")

	// The staff account must be left unchanged.
	stored := admin.byID["s1"]
	if stored.Role != RoleOwner || !stored.IsActive {
		t.Fatalf("expected staff to be unchanged after a rejected update, got role=%q is_active=%v",
			stored.Role, stored.IsActive)
	}
}

func TestUpdateStaffLastOwnerRejectsDeactivate(t *testing.T) {
	owner := &Staff{ID: "s1", Phone: "+996700000001", Name: "Owner", Role: RoleOwner, IsActive: true}
	admin := newFakeStaffAdmin(owner)
	svc := newTestAdminService(admin)

	_, err := svc.UpdateStaff(context.Background(), "s1", UpdateStaffInput{
		Name: "Owner", Role: RoleOwner, PointID: nil, IsActive: false,
	})
	assertAppErrCode(t, err, "last_owner")

	stored := admin.byID["s1"]
	if stored.Role != RoleOwner || !stored.IsActive {
		t.Fatalf("expected staff to be unchanged after a rejected update, got role=%q is_active=%v",
			stored.Role, stored.IsActive)
	}
}

func TestUpdateStaffLastOwnerAllowsWhenAnotherActiveOwnerExists(t *testing.T) {
	owner1 := &Staff{ID: "s1", Phone: "+996700000001", Name: "Owner One", Role: RoleOwner, IsActive: true}
	owner2 := &Staff{ID: "s2", Phone: "+996700000002", Name: "Owner Two", Role: RoleOwner, IsActive: true}
	admin := newFakeStaffAdmin(owner1, owner2)
	svc := newTestAdminService(admin)

	updated, err := svc.UpdateStaff(context.Background(), "s1", UpdateStaffInput{
		Name: "Owner One", Role: RoleManager, PointID: nil, IsActive: true,
	})
	if err != nil {
		t.Fatalf("UpdateStaff() unexpected error: %v", err)
	}
	if updated.Role != RoleManager {
		t.Fatalf("expected role to be updated to manager, got %q", updated.Role)
	}
}

func TestUpdateStaffLastOwnerIgnoresInactiveOwners(t *testing.T) {
	// A previously-deactivated owner doesn't count as "another active
	// owner" — demoting/deactivating the one remaining active owner must
	// still be rejected.
	activeOwner := &Staff{ID: "s1", Phone: "+996700000001", Name: "Active Owner", Role: RoleOwner, IsActive: true}
	inactiveOwner := &Staff{ID: "s2", Phone: "+996700000002", Name: "Inactive Owner", Role: RoleOwner, IsActive: false}
	admin := newFakeStaffAdmin(activeOwner, inactiveOwner)
	svc := newTestAdminService(admin)

	_, err := svc.UpdateStaff(context.Background(), "s1", UpdateStaffInput{
		Name: "Active Owner", Role: RoleManager, PointID: nil, IsActive: true,
	})
	assertAppErrCode(t, err, "last_owner")
}

func TestUpdateStaffLastOwnerDoesNotApplyToNonOwners(t *testing.T) {
	// Deactivating a manager (not an owner) is unaffected by the
	// last-owner rule, even if there happen to be zero owners at all.
	manager := &Staff{ID: "s1", Phone: "+996700000001", Name: "Manager", Role: RoleManager, IsActive: true}
	admin := newFakeStaffAdmin(manager)
	svc := newTestAdminService(admin)

	updated, err := svc.UpdateStaff(context.Background(), "s1", UpdateStaffInput{
		Name: "Manager", Role: RoleManager, PointID: nil, IsActive: false,
	})
	if err != nil {
		t.Fatalf("UpdateStaff() unexpected error: %v", err)
	}
	if updated.IsActive {
		t.Fatal("expected manager to be deactivated")
	}
}

// --- point_id / role validation ---

func TestUpdateStaffPointIDRequiredForPointStaff(t *testing.T) {
	staffer := &Staff{ID: "s1", Phone: "+996700000001", Name: "Clerk", Role: RolePointStaff, PointID: strPtr("p1"), IsActive: true}
	admin := newFakeStaffAdmin(staffer)
	svc := newTestAdminService(admin)

	_, err := svc.UpdateStaff(context.Background(), "s1", UpdateStaffInput{
		Name: "Clerk", Role: RolePointStaff, PointID: nil, IsActive: true,
	})
	assertAppErrCode(t, err, "point_id_required")
}

func TestUpdateStaffPointIDForbiddenForOwner(t *testing.T) {
	staffer := &Staff{ID: "s1", Phone: "+996700000001", Name: "Boss", Role: RoleManager, IsActive: true}
	admin := newFakeStaffAdmin(staffer)
	svc := newTestAdminService(admin)

	_, err := svc.UpdateStaff(context.Background(), "s1", UpdateStaffInput{
		Name: "Boss", Role: RoleOwner, PointID: strPtr("p1"), IsActive: true,
	})
	assertAppErrCode(t, err, "point_id_forbidden")
}

func TestUpdateStaffPointIDForbiddenForManager(t *testing.T) {
	staffer := &Staff{ID: "s1", Phone: "+996700000001", Name: "Manager", Role: RoleManager, IsActive: true}
	admin := newFakeStaffAdmin(staffer)
	svc := newTestAdminService(admin)

	_, err := svc.UpdateStaff(context.Background(), "s1", UpdateStaffInput{
		Name: "Manager", Role: RoleManager, PointID: strPtr("p1"), IsActive: true,
	})
	assertAppErrCode(t, err, "point_id_forbidden")
}

func TestCreateStaffPointIDRequiredForPointStaff(t *testing.T) {
	admin := newFakeStaffAdmin()
	svc := newTestAdminService(admin)

	_, err := svc.CreateStaff(context.Background(), CreateStaffInput{
		Phone: "+996700000009", Password: "secret123", Name: "Clerk", Role: RolePointStaff, PointID: nil,
	})
	assertAppErrCode(t, err, "point_id_required")
}

func TestCreateStaffPointIDForbiddenForOwner(t *testing.T) {
	admin := newFakeStaffAdmin()
	svc := newTestAdminService(admin)

	_, err := svc.CreateStaff(context.Background(), CreateStaffInput{
		Phone: "+996700000009", Password: "secret123", Name: "Boss", Role: RoleOwner, PointID: strPtr("p1"),
	})
	assertAppErrCode(t, err, "point_id_forbidden")
}

func TestCreateStaffRejectsUnknownRole(t *testing.T) {
	admin := newFakeStaffAdmin()
	svc := newTestAdminService(admin)

	_, err := svc.CreateStaff(context.Background(), CreateStaffInput{
		Phone: "+996700000009", Password: "secret123", Name: "Whoever", Role: Role("superadmin"), PointID: nil,
	})
	assertAppErrCode(t, err, "invalid_role")
}

// --- ordinary create/update succeed ---

func TestUpdateStaffOrdinaryUpdateSucceeds(t *testing.T) {
	manager := &Staff{ID: "s1", Phone: "+996700000001", Name: "Old Name", Role: RoleManager, IsActive: true}
	admin := newFakeStaffAdmin(manager)
	svc := newTestAdminService(admin)

	updated, err := svc.UpdateStaff(context.Background(), "s1", UpdateStaffInput{
		Name: "New Name", Role: RoleManager, PointID: nil, IsActive: true,
	})
	if err != nil {
		t.Fatalf("UpdateStaff() unexpected error: %v", err)
	}
	if updated.Name != "New Name" {
		t.Fatalf("expected name to be updated, got %q", updated.Name)
	}
}

func TestUpdateStaffChangesPassword(t *testing.T) {
	manager := &Staff{ID: "s1", Phone: "+996700000001", Name: "Manager", PasswordHash: mustHash(t, "old-pass"), Role: RoleManager, IsActive: true}
	admin := newFakeStaffAdmin(manager)
	svc := newTestAdminService(admin)

	newPassword := "new-strong-pass"
	updated, err := svc.UpdateStaff(context.Background(), "s1", UpdateStaffInput{
		Name: "Manager", Role: RoleManager, PointID: nil, IsActive: true, Password: &newPassword,
	})
	if err != nil {
		t.Fatalf("UpdateStaff() unexpected error: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(updated.PasswordHash), []byte(newPassword)); err != nil {
		t.Fatalf("expected the stored hash to match the new password: %v", err)
	}
}

func TestUpdateStaffLeavesPasswordUnchangedWhenOmitted(t *testing.T) {
	oldHash := mustHash(t, "old-pass")
	manager := &Staff{ID: "s1", Phone: "+996700000001", Name: "Manager", PasswordHash: oldHash, Role: RoleManager, IsActive: true}
	admin := newFakeStaffAdmin(manager)
	svc := newTestAdminService(admin)

	updated, err := svc.UpdateStaff(context.Background(), "s1", UpdateStaffInput{
		Name: "Manager", Role: RoleManager, PointID: nil, IsActive: true, Password: nil,
	})
	if err != nil {
		t.Fatalf("UpdateStaff() unexpected error: %v", err)
	}
	if updated.PasswordHash != oldHash {
		t.Fatal("expected the password hash to be left unchanged when Password is nil")
	}
}

func TestCreateStaffHashesPassword(t *testing.T) {
	admin := newFakeStaffAdmin()
	svc := newTestAdminService(admin)

	created, err := svc.CreateStaff(context.Background(), CreateStaffInput{
		Phone: "+996700000009", Password: "plaintext-pass", Name: "Clerk", Role: RolePointStaff, PointID: strPtr("p1"),
	})
	if err != nil {
		t.Fatalf("CreateStaff() unexpected error: %v", err)
	}
	if created.PasswordHash == "plaintext-pass" {
		t.Fatal("expected the password to be bcrypt-hashed, not stored as plaintext")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(created.PasswordHash), []byte("plaintext-pass")); err != nil {
		t.Fatalf("expected the stored hash to match the given password: %v", err)
	}
	if !created.IsActive {
		t.Fatal("expected a newly created staff account to be active")
	}
}

func TestListStaffReturnsAll(t *testing.T) {
	s1 := &Staff{ID: "s1", Phone: "+996700000001", Name: "A", Role: RoleOwner, IsActive: true}
	s2 := &Staff{ID: "s2", Phone: "+996700000002", Name: "B", Role: RoleManager, IsActive: false}
	admin := newFakeStaffAdmin(s1, s2)
	svc := newTestAdminService(admin)

	list, err := svc.ListStaff(context.Background())
	if err != nil {
		t.Fatalf("ListStaff() unexpected error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 staff accounts, got %d", len(list))
	}
}
