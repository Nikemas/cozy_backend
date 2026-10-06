package staff

import (
	"context"
	"testing"
)

func TestCanonicalPhoneKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"+996700123456", "+996700123456"},
		{"996700123456", "+996700123456"},
		{"0700123456", "+996700123456"},
		{" 0700 123-456 ", "+996700123456"},
		{"+996 (700) 12 34 56", "+996700123456"},
		// Not a Kyrgyz number: kept as typed (trimmed) so a legacy row
		// stored in that exact form can still be matched.
		{"  +7 999 123  ", "+7 999 123"},
		{"", ""},
	}
	for _, c := range cases {
		if got := canonicalPhoneKey(c.in); got != c.want {
			t.Errorf("canonicalPhoneKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPhoneDigitsSuffix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"+996700123456", "700123456"},
		{"0700 123 456", "700123456"},
		{"+7 999", "7999"},
		{"", ""},
	}
	for _, c := range cases {
		if got := phoneDigitsSuffix(c.in); got != c.want {
			t.Errorf("phoneDigitsSuffix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLoginAcceptsAnySpellingOfStoredPhone(t *testing.T) {
	for _, typed := range []string{"+996700000001", "996700000001", "0700000001", "0700 000 001", "+996 700 00-00-01"} {
		t.Run(typed, func(t *testing.T) {
			st := &Staff{ID: "s1", Phone: "+996700000001", PasswordHash: mustHash(t, "correct-horse"), Role: RoleManager, IsActive: true}
			svc, _ := newTestService(t, st)

			if _, err := svc.Login(context.Background(), typed, "correct-horse"); err != nil {
				t.Fatalf("Login(%q) unexpected error: %v", typed, err)
			}
		})
	}
}

func TestLoginMatchesLegacyStoredFormat(t *testing.T) {
	// A row created before normalization, stored in local format.
	legacy := &Staff{ID: "s1", Phone: "0700 000 001", PasswordHash: mustHash(t, "correct-horse"), Role: RoleOwner, IsActive: true}
	svc, _ := newTestService(t, legacy)

	if _, err := svc.Login(context.Background(), "+996700000001", "correct-horse"); err != nil {
		t.Fatalf("Login() against a legacy-format row: %v", err)
	}
}

func TestLoginRateLimitKeyedOnNormalizedPhone(t *testing.T) {
	st := &Staff{ID: "s1", Phone: "+996700000001", PasswordHash: mustHash(t, "correct-horse"), Role: RoleManager, IsActive: true}
	svc, _ := newTestService(t, st)
	spellings := []string{"+996700000001", "0700000001", "996700000001", "0700 000 001", "+996 700 000 001"}

	for _, p := range spellings[:loginAttemptLimit] {
		_, err := svc.Login(context.Background(), p, "wrong-password")
		assertUnauthorized(t, err)
	}

	// Budget exhausted for the number, whatever spelling comes next —
	// even with the right password.
	_, err := svc.Login(context.Background(), "0700-000-001", "correct-horse")
	assertAppErrCode(t, err, "too_many_attempts")
}

func TestCreateStaffStoresNormalizedPhone(t *testing.T) {
	admin := newFakeStaffAdmin()
	svc := newTestAdminService(admin)

	created, err := svc.CreateStaff(context.Background(), CreateStaffInput{
		Phone: "0555 123 456", Password: "secret123", Name: "Clerk", Role: RoleManager,
	})
	if err != nil {
		t.Fatalf("CreateStaff() unexpected error: %v", err)
	}
	if created.Phone != "+996555123456" {
		t.Fatalf("stored phone = %q, want +996555123456", created.Phone)
	}
}

func TestCreateStaffRejectsInvalidPhone(t *testing.T) {
	svc := newTestAdminService(newFakeStaffAdmin())

	_, err := svc.CreateStaff(context.Background(), CreateStaffInput{
		Phone: "12345", Password: "secret123", Name: "Clerk", Role: RoleManager,
	})
	assertAppErrCode(t, err, "invalid_phone")
}

func TestCreateStaffDuplicateInAnotherSpellingConflicts(t *testing.T) {
	existing := &Staff{ID: "s1", Phone: "+996555123456", Name: "Clerk", Role: RoleManager, IsActive: true}
	svc := newTestAdminService(newFakeStaffAdmin(existing))

	_, err := svc.CreateStaff(context.Background(), CreateStaffInput{
		Phone: "0555123456", Password: "secret123", Name: "Clerk 2", Role: RoleManager,
	})
	assertAppErrCode(t, err, "phone_taken")
}
