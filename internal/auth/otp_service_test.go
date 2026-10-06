package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
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

const (
	reviewPhone = "+996700999111"
	reviewCode  = "7391"
)

func reviewLogin() config.ReviewLogin {
	return config.ReviewLogin{Phone: reviewPhone, Code: reviewCode}
}

// countingSMS fails the test if the provider is touched.
type countingSMS struct {
	fakeSMS
	sends int
}

func (c *countingSMS) SendCode(ctx context.Context, phone, tx string) (string, error) {
	c.sends++
	return c.fakeSMS.SendCode(ctx, phone, tx)
}

func newReviewService(store *fakeOTPStore, sms *countingSMS, review config.ReviewLogin) *Service {
	svc := newTestService(store, &sms.fakeSMS, testLimits)
	svc.sms = sms
	svc.review = review
	return svc
}

func TestReviewPhoneSkipsSMSAndVerifiesWithConfiguredCode(t *testing.T) {
	store, sms := newFakeOTPStore(), &countingSMS{}
	svc := newReviewService(store, sms, reviewLogin())
	ctx := context.Background()

	// Any accepted spelling of the number is the same review phone.
	if err := svc.RequestOTP(ctx, "0700999111"); err != nil {
		t.Fatal(err)
	}
	if sms.sends != 0 {
		t.Fatalf("SMS provider called %d times for the review phone", sms.sends)
	}
	if strings.Contains(store.rows["otp-1"].token, reviewCode) {
		t.Error("stored token must not contain the review code")
	}
	access, refresh, cust, err := svc.VerifyOTP(ctx, reviewPhone, reviewCode)
	if err != nil {
		t.Fatal(err)
	}
	if access == "" || refresh == "" || cust.Phone != reviewPhone {
		t.Errorf("unexpected login result: customer %+v", cust)
	}
	// One-time: the row is consumed.
	_, _, _, err = svc.VerifyOTP(ctx, reviewPhone, reviewCode)
	wantAppErr(t, err, http.StatusBadRequest, "otp_expired")
}

func TestReviewPhoneStillSubjectToAttemptLimit(t *testing.T) {
	store, sms := newFakeOTPStore(), &countingSMS{}
	svc := newReviewService(store, sms, reviewLogin())
	ctx := context.Background()
	if err := svc.RequestOTP(ctx, reviewPhone); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		_, _, _, err := svc.VerifyOTP(ctx, reviewPhone, "0000")
		wantAppErr(t, err, http.StatusBadRequest, "otp_invalid")
	}
	_, _, _, err := svc.VerifyOTP(ctx, reviewPhone, "0000")
	wantAppErr(t, err, http.StatusTooManyRequests, "otp_attempts_exceeded")
	// Burned: even the right code no longer works.
	_, _, _, err = svc.VerifyOTP(ctx, reviewPhone, reviewCode)
	wantAppErr(t, err, http.StatusBadRequest, "otp_expired")
}

func TestReviewPhoneRejectsProviderCode(t *testing.T) {
	store, sms := newFakeOTPStore(), &countingSMS{}
	svc := newReviewService(store, sms, reviewLogin())
	ctx := context.Background()
	if err := svc.RequestOTP(ctx, reviewPhone); err != nil {
		t.Fatal(err)
	}
	// "1234" is what the fake provider accepts; it must not work here.
	_, _, _, err := svc.VerifyOTP(ctx, reviewPhone, "1234")
	wantAppErr(t, err, http.StatusBadRequest, "otp_invalid")
}

func TestReviewCodeDoesNotLogInOtherPhones(t *testing.T) {
	store, sms := newFakeOTPStore(), &countingSMS{}
	svc := newReviewService(store, sms, reviewLogin())
	ctx := context.Background()

	if err := svc.RequestOTP(ctx, "0700123456"); err != nil {
		t.Fatal(err)
	}
	if sms.sends != 1 {
		t.Fatalf("other phone must get a real SMS, sends = %d", sms.sends)
	}
	_, _, _, err := svc.VerifyOTP(ctx, "0700123456", reviewCode)
	wantAppErr(t, err, http.StatusBadRequest, "otp_invalid")
	if _, _, _, err := svc.VerifyOTP(ctx, "0700123456", "1234"); err != nil {
		t.Fatalf("other phone with provider code: %v", err)
	}
}

func TestReviewMarkerTokenOnOtherPhoneNeverUsesReviewCode(t *testing.T) {
	store, sms := newFakeOTPStore(), &countingSMS{}
	svc := newReviewService(store, sms, reviewLogin())
	// A row for another phone that happens to carry the review marker.
	store.nextID = 1
	store.rows["otp-1"] = &fakeOTPRow{phone: "+996700123456", token: reviewTokenPrefix + "x"}

	_, _, _, err := svc.VerifyOTP(context.Background(), "0700123456", reviewCode)
	wantAppErr(t, err, http.StatusBadRequest, "otp_invalid")
}

func TestReviewDisabledBehavesAsBefore(t *testing.T) {
	store, sms := newFakeOTPStore(), &countingSMS{}
	svc := newReviewService(store, sms, config.ReviewLogin{})
	ctx := context.Background()

	if err := svc.RequestOTP(ctx, reviewPhone); err != nil {
		t.Fatal(err)
	}
	if sms.sends != 1 {
		t.Fatalf("disabled: SMS must be sent, sends = %d", sms.sends)
	}
	_, _, _, err := svc.VerifyOTP(ctx, reviewPhone, reviewCode)
	wantAppErr(t, err, http.StatusBadRequest, "otp_invalid")
}

func TestReviewPhoneIgnoresStaleProviderRow(t *testing.T) {
	// A row issued by the provider before the feature was enabled must be
	// verified by the provider, not by the review code.
	store, sms := newFakeOTPStore(), &countingSMS{}
	svc := newReviewService(store, sms, config.ReviewLogin{})
	if err := svc.RequestOTP(context.Background(), reviewPhone); err != nil {
		t.Fatal(err)
	}
	svc.review = reviewLogin()
	_, _, _, err := svc.VerifyOTP(context.Background(), reviewPhone, reviewCode)
	wantAppErr(t, err, http.StatusBadRequest, "otp_invalid")
}
