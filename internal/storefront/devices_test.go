package storefront

import (
	"context"
	"testing"
)

// validPlatform is the only device-token logic that doesn't need a live
// database — DeviceTokenRepo.Register is a thin SQL wrapper around it and
// isn't covered here (no Postgres available in this environment; see
// addresses_test.go's note for the same reasoning).

func TestValidPlatformAcceptsIOSAndAndroid(t *testing.T) {
	for _, p := range []string{PlatformIOS, PlatformAndroid} {
		if !validPlatform(p) {
			t.Errorf("validPlatform(%q) = false, want true", p)
		}
	}
}

func TestValidPlatformRejectsAnythingElse(t *testing.T) {
	for _, p := range []string{"", "web", "IOS", "Android", "ios "} {
		if validPlatform(p) {
			t.Errorf("validPlatform(%q) = true, want false", p)
		}
	}
}

func TestRegisterRejectsInvalidPlatformBeforeTouchingDB(t *testing.T) {
	// A nil *sql.DB would panic if Register ever reached r.db.ExecContext,
	// so this also proves the platform check runs first.
	repo := NewDeviceTokenRepo(nil)
	err := repo.Register(context.Background(), "customer-1", "token-1", "windows-phone")
	if err == nil {
		t.Fatal("Register() with invalid platform: got nil error, want one")
	}
}
