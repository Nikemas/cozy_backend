package auth

import (
	"context"
	"net/http"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// login runs a full OTP login and returns the token pair.
func login(t *testing.T, svc *Service, phone string) (string, string) {
	t.Helper()
	ctx := context.Background()
	if err := svc.RequestOTP(ctx, phone); err != nil {
		t.Fatal(err)
	}
	access, refresh, c, err := svc.VerifyOTP(ctx, phone, "1234")
	if err != nil || access == "" || refresh == "" || c == nil {
		t.Fatalf("VerifyOTP = %q %q %v %v", access, refresh, c, err)
	}
	return access, refresh
}

func TestRefreshRotates(t *testing.T) {
	svc := newTestService(newFakeOTPStore(), &fakeSMS{}, testLimits)
	store := svc.refresh.(*fakeRefreshStore)
	_, r1 := login(t, svc, "0700123456")

	access, r2, err := svc.Refresh(context.Background(), r1)
	if err != nil || access == "" || r2 == "" || r2 == r1 {
		t.Fatalf("Refresh = %q %q %v", access, r2, err)
	}
	if id, err := ParseAccessToken(testJWTSecret, access); err != nil || id != "cust-+996700123456" {
		t.Errorf("access token customer = %q %v", id, err)
	}
	if store.active(r1) || !store.active(r2) {
		t.Error("old token must be retired, new one active")
	}
}

func TestRefreshReuseRevokesWholeFamily(t *testing.T) {
	svc := newTestService(newFakeOTPStore(), &fakeSMS{}, testLimits)
	store := svc.refresh.(*fakeRefreshStore)
	_, r1 := login(t, svc, "0700123456")
	_, other := login(t, svc, "0700999999") // unrelated session

	_, r2, err := svc.Refresh(context.Background(), r1)
	if err != nil {
		t.Fatal(err)
	}
	// r1 presented again: reuse.
	_, _, err = svc.Refresh(context.Background(), r1)
	wantAppErr(t, err, http.StatusUnauthorized, "refresh_invalid")
	if store.active(r2) {
		t.Error("reuse must revoke the successor token too")
	}
	_, _, err = svc.Refresh(context.Background(), r2)
	wantAppErr(t, err, http.StatusUnauthorized, "refresh_invalid")
	if !store.active(other) {
		t.Error("other families must be untouched")
	}
}

func TestRefreshLostResponseRetryWithinGrace(t *testing.T) {
	svc := newTestService(newFakeOTPStore(), &fakeSMS{}, testLimits)
	svc.refreshReuseGrace = time.Minute
	store := svc.refresh.(*fakeRefreshStore)
	_, r1 := login(t, svc, "0700123456")

	_, r2, err := svc.Refresh(context.Background(), r1) // response "lost"
	if err != nil {
		t.Fatal(err)
	}
	access, r3, err := svc.Refresh(context.Background(), r1) // app retries with r1
	if err != nil || access == "" || r3 == "" || r3 == r2 {
		t.Fatalf("retry within grace = %q %q %v", access, r3, err)
	}
	if !store.active(r2) || !store.active(r3) {
		t.Error("retry within grace must not revoke the family")
	}
}

func TestRefreshRetryWithinGraceAfterLogoutIsInvalid(t *testing.T) {
	svc := newTestService(newFakeOTPStore(), &fakeSMS{}, testLimits)
	svc.refreshReuseGrace = time.Minute
	store := svc.refresh.(*fakeRefreshStore)
	_, r1 := login(t, svc, "0700123456")

	_, r2, err := svc.Refresh(context.Background(), r1)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(context.Background(), r2); err != nil {
		t.Fatal(err)
	}
	_, _, err = svc.Refresh(context.Background(), r1)
	wantAppErr(t, err, http.StatusUnauthorized, "refresh_invalid")
	if store.active(r2) {
		t.Error("logged-out session must stay revoked")
	}
}

func TestRefreshInvalidTokens(t *testing.T) {
	svc := newTestService(newFakeOTPStore(), &fakeSMS{}, testLimits)
	for _, tok := range []string{"", "never-issued"} {
		_, _, err := svc.Refresh(context.Background(), tok)
		wantAppErr(t, err, http.StatusUnauthorized, "refresh_invalid")
	}
}

func TestRefreshRateLimitedPerIP(t *testing.T) {
	limits := testLimits
	limits.RefreshPerIPPerMinute = 2
	svc := newTestService(newFakeOTPStore(), &fakeSMS{}, limits)
	ctx := ipCtx("198.51.100.7")
	for i := 0; i < 2; i++ {
		_, _, err := svc.Refresh(ctx, "x")
		wantAppErr(t, err, http.StatusUnauthorized, "refresh_invalid")
	}
	_, _, err := svc.Refresh(ctx, "x")
	wantAppErr(t, err, http.StatusTooManyRequests, "refresh_rate_limited")
	_, _, err = svc.Refresh(ipCtx("203.0.113.1"), "x")
	wantAppErr(t, err, http.StatusUnauthorized, "refresh_invalid")
}

func TestLogoutRevokesAndIsIdempotent(t *testing.T) {
	svc := newTestService(newFakeOTPStore(), &fakeSMS{}, testLimits)
	store := svc.refresh.(*fakeRefreshStore)
	_, r1 := login(t, svc, "0700123456")
	_, r2, err := svc.Refresh(context.Background(), r1)
	if err != nil {
		t.Fatal(err)
	}

	for _, tok := range []string{r2, r2, "", "garbage"} {
		if err := svc.Logout(context.Background(), tok); err != nil {
			t.Fatalf("Logout(%q) = %v", tok, err)
		}
	}
	if store.active(r2) {
		t.Error("logout must revoke the token")
	}
	_, _, err = svc.Refresh(context.Background(), r2)
	wantAppErr(t, err, http.StatusUnauthorized, "refresh_invalid")
}

func TestVerifyOTPFromAnotherIPUnaffectedByBlockedIP(t *testing.T) {
	limits := testLimits
	limits.OTPVerifyFailsPerIPPerHour = 1
	svc := newTestService(newFakeOTPStore(), &fakeSMS{}, limits)
	if err := svc.RequestOTP(context.Background(), "0700123456"); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := svc.VerifyOTP(ipCtx("198.51.100.7"), "0700123456", "0000")
	wantAppErr(t, err, http.StatusBadRequest, "otp_invalid")
	_, _, _, err = svc.VerifyOTP(ipCtx("198.51.100.7"), "0700123456", "1234")
	wantAppErr(t, err, http.StatusTooManyRequests, "otp_attempts_exceeded")
	if _, _, _, err := svc.VerifyOTP(ipCtx("203.0.113.1"), "0700123456", "1234"); err != nil {
		t.Fatalf("other IP: %v", err)
	}
	// The code is single-use.
	_, _, _, err = svc.VerifyOTP(ipCtx("203.0.113.1"), "0700123456", "1234")
	wantAppErr(t, err, http.StatusBadRequest, "otp_expired")
}

func TestRefreshRepoRotateSQL(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	repo := newRefreshRepo(db)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`UPDATE refresh_tokens SET revoked_at = now(), rotated_at = now()
			WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
			RETURNING id, customer_id, family_id`)).
		WithArgs("old").
		WillReturnRows(sqlmock.NewRows([]string{"id", "customer_id", "family_id"}).AddRow("rt-1", "cust-1", "fam-1"))
	mock.ExpectExec(`INSERT INTO refresh_tokens \(customer_id, token_hash, expires_at, family_id\)`).
		WithArgs("cust-1", "new", sqlmock.AnyArg(), "fam-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	got, err := repo.rotate(context.Background(), "old", "new", time.Now().Add(time.Hour))
	if err != nil || got == nil || got.FamilyID != "fam-1" || got.CustomerID != "cust-1" {
		t.Fatalf("rotate = %+v, %v", got, err)
	}

	// Inactive token: no insert, nil result.
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE refresh_tokens SET revoked_at`).WithArgs("gone").
		WillReturnRows(sqlmock.NewRows([]string{"id", "customer_id", "family_id"}))
	mock.ExpectCommit()
	if got, err := repo.rotate(context.Background(), "gone", "new2", time.Now()); err != nil || got != nil {
		t.Fatalf("rotate inactive = %+v, %v", got, err)
	}

	mock.ExpectExec(regexp.QuoteMeta(`UPDATE refresh_tokens SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL`)).
		WithArgs("fam-1").WillReturnResult(sqlmock.NewResult(0, 2))
	if err := repo.revokeFamily(context.Background(), "fam-1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
