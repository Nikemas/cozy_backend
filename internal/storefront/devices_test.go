package storefront

import (
	"context"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func TestTokensForCustomerAndDeleteToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	repo := NewDeviceTokenRepo(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT fcm_token FROM device_tokens WHERE customer_id = $1")).
		WithArgs("cust-1").
		WillReturnRows(sqlmock.NewRows([]string{"fcm_token"}).AddRow("tok-a").AddRow("tok-b"))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM device_tokens WHERE fcm_token = $1")).
		WithArgs("tok-a").
		WillReturnResult(sqlmock.NewResult(0, 1))

	tokens, err := repo.TokensForCustomer(context.Background(), "cust-1")
	if err != nil {
		t.Fatalf("TokensForCustomer: %v", err)
	}
	if len(tokens) != 2 || tokens[0] != "tok-a" || tokens[1] != "tok-b" {
		t.Errorf("tokens = %v", tokens)
	}
	if err := repo.DeleteToken(context.Background(), "tok-a"); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

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
