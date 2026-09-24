package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/httpmw"
)

var testLimits = config.AuthLimits{OTPVerifyMaxAttempts: 5, OTPVerifyFailsPerIPPerHour: 30, RefreshPerIPPerMinute: 120}

func ipCtx(ip string) context.Context { return httpmw.WithClientIP(context.Background(), ip) }

func TestRequestOTPPassesClientIPAndStoresToken(t *testing.T) {
	store := newFakeOTPStore()
	svc := newTestService(store, &fakeSMS{}, testLimits)

	if err := svc.RequestOTP(ipCtx("198.51.100.7"), "0700123456"); err != nil {
		t.Fatal(err)
	}
	if store.lastIP != "198.51.100.7" {
		t.Errorf("reserve got ip %q", store.lastIP)
	}
	if store.rows["otp-1"].token == "" {
		t.Error("provider token not stored")
	}
}

func TestRequestOTPBurnsRowWhenSendFails(t *testing.T) {
	store := newFakeOTPStore()
	svc := newTestService(store, &fakeSMS{sendErr: errProviderDown}, testLimits)

	if err := svc.RequestOTP(context.Background(), "0700123456"); !errors.Is(err, errProviderDown) {
		t.Fatalf("err = %v", err)
	}
	if !store.rows["otp-1"].consumed {
		t.Error("row of a failed send must be burned")
	}
}

func TestVerifyOTPBurnsCodeAfterMaxAttempts(t *testing.T) {
	store := newFakeOTPStore()
	svc := newTestService(store, &fakeSMS{}, testLimits)
	ctx := context.Background()
	if err := svc.RequestOTP(ctx, "0700123456"); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 4; i++ {
		_, _, _, err := svc.VerifyOTP(ctx, "0700123456", "0000")
		wantAppErr(t, err, http.StatusBadRequest, "otp_invalid")
	}
	_, _, _, err := svc.VerifyOTP(ctx, "0700123456", "0000")
	wantAppErr(t, err, http.StatusTooManyRequests, "otp_attempts_exceeded")

	// Even the right code no longer works: the code is burned.
	_, _, _, err = svc.VerifyOTP(ctx, "0700123456", "1234")
	wantAppErr(t, err, http.StatusBadRequest, "otp_expired")
}

func TestVerifyOTPProviderOutageDoesNotCountAgainstIP(t *testing.T) {
	store := newFakeOTPStore()
	sms := &fakeSMS{}
	limits := testLimits
	limits.OTPVerifyFailsPerIPPerHour = 1
	svc := newTestService(store, sms, limits)
	ctx := ipCtx("198.51.100.7")
	if err := svc.RequestOTP(ctx, "0700123456"); err != nil {
		t.Fatal(err)
	}

	sms.verifyErr = errProviderDown
	if _, _, _, err := svc.VerifyOTP(ctx, "0700123456", "1234"); !errors.Is(err, errProviderDown) {
		t.Fatalf("err = %v", err)
	}
	if svc.verifyFails.blocked("198.51.100.7") {
		t.Error("a provider outage must not count as a wrong code")
	}
}

func TestVerifyOTPPerIPFailureLimitSpansPhones(t *testing.T) {
	store := newFakeOTPStore()
	limits := testLimits
	limits.OTPVerifyFailsPerIPPerHour = 2
	svc := newTestService(store, &fakeSMS{}, limits)
	ctx := ipCtx("198.51.100.7")

	for _, phone := range []string{"0700000001", "0700000002", "0700000003"} {
		if err := svc.RequestOTP(ctx, phone); err != nil {
			t.Fatal(err)
		}
	}
	_, _, _, err := svc.VerifyOTP(ctx, "0700000001", "0000")
	wantAppErr(t, err, http.StatusBadRequest, "otp_invalid")
	_, _, _, err = svc.VerifyOTP(ctx, "0700000002", "0000")
	wantAppErr(t, err, http.StatusBadRequest, "otp_invalid")
	// Third phone, right code — but this IP is out of wrong guesses.
	_, _, _, err = svc.VerifyOTP(ctx, "0700000003", "1234")
	wantAppErr(t, err, http.StatusTooManyRequests, "otp_attempts_exceeded")

}
