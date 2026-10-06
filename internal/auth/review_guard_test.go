package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingMessenger captures ops alerts.
type recordingMessenger struct {
	mu   sync.Mutex
	msgs []string
	sent chan struct{}
}

func newRecordingMessenger() *recordingMessenger {
	return &recordingMessenger{sent: make(chan struct{}, 16)}
}

func (r *recordingMessenger) SendStaffMessage(_ context.Context, text string) error {
	r.mu.Lock()
	r.msgs = append(r.msgs, text)
	r.mu.Unlock()
	r.sent <- struct{}{}
	return nil
}

func (r *recordingMessenger) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.msgs...)
}

func (r *recordingMessenger) waitFor(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-r.sent:
		case <-time.After(2 * time.Second):
			t.Fatalf("alert %d not sent", i+1)
		}
	}
}

// failReviewLogins issues a fresh review OTP and guesses wrong n times,
// one guess per OTP row so the per-row attempt limit never kicks in.
func failReviewLogins(t *testing.T, svc *Service, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := svc.RequestOTP(ctx, reviewPhone); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := svc.VerifyOTP(ctx, reviewPhone, "0000")
		wantAppErr(t, err, http.StatusBadRequest, "otp_invalid")
	}
}

func TestReviewLoginWorksBelowFailureLimit(t *testing.T) {
	store, sms := newFakeOTPStore(), &countingSMS{}
	svc := newReviewService(store, sms, reviewLogin())
	failReviewLogins(t, svc, reviewMaxFailures-1)

	if err := svc.RequestOTP(context.Background(), reviewPhone); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.VerifyOTP(context.Background(), reviewPhone, reviewCode); err != nil {
		t.Fatalf("review code after %d failures: %v", reviewMaxFailures-1, err)
	}
	if sms.sends != 0 {
		t.Fatalf("SMS sent below the limit: %d", sms.sends)
	}
}

func TestReviewLoginDisabledAfterFailureLimit(t *testing.T) {
	store, sms := newFakeOTPStore(), &countingSMS{}
	svc := newReviewService(store, sms, reviewLogin())
	failReviewLogins(t, svc, reviewMaxFailures)
	ctx := context.Background()

	// The review phone is now an ordinary phone: a real SMS is sent and
	// only the provider's code works; the fixed review code never does.
	if err := svc.RequestOTP(ctx, reviewPhone); err != nil {
		t.Fatal(err)
	}
	if sms.sends != 1 {
		t.Fatalf("after lockout the review phone must use SMS, sends = %d", sms.sends)
	}
	_, _, _, err := svc.VerifyOTP(ctx, reviewPhone, reviewCode)
	wantAppErr(t, err, http.StatusBadRequest, "otp_invalid")
	if _, _, _, err := svc.VerifyOTP(ctx, reviewPhone, "1234"); err != nil {
		t.Fatalf("provider code after lockout: %v", err)
	}
}

func TestReviewLockoutRejectsStillActiveReviewRow(t *testing.T) {
	store, sms := newFakeOTPStore(), &countingSMS{}
	svc := newReviewService(store, sms, reviewLogin())
	ctx := context.Background()
	failReviewLogins(t, svc, reviewMaxFailures-1)

	// A review row is issued, then the 10th failure (on a newer row)
	// trips the lockout; the earlier row must not accept the code either.
	if err := svc.RequestOTP(ctx, reviewPhone); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	issued := store.nextID
	store.mu.Unlock()
	failReviewLogins(t, svc, 1)
	store.mu.Lock()
	for i := issued + 1; i <= store.nextID; i++ {
		store.rows[fmt.Sprintf("otp-%d", i)].consumed = true
	}
	store.mu.Unlock()

	_, _, _, err := svc.VerifyOTP(ctx, reviewPhone, reviewCode)
	wantAppErr(t, err, http.StatusBadRequest, "otp_invalid")
}

func TestReviewLockoutAlertsOnceWithoutCode(t *testing.T) {
	store, sms := newFakeOTPStore(), &countingSMS{}
	svc := newReviewService(store, sms, reviewLogin())
	alerts := newRecordingMessenger()
	svc.opsAlerts = alerts

	failReviewLogins(t, svc, reviewMaxFailures)
	alerts.waitFor(t, 1)
	// More wrong codes after the lockout (now via the provider) must not
	// alert again.
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := svc.RequestOTP(ctx, reviewPhone); err != nil {
			t.Fatal(err)
		}
		_, _, _, _ = svc.VerifyOTP(ctx, reviewPhone, reviewCode)
	}
	time.Sleep(50 * time.Millisecond)

	msgs := alerts.messages()
	if len(msgs) != 1 {
		t.Fatalf("alerts = %d, want 1: %v", len(msgs), msgs)
	}
	if strings.Contains(msgs[0], reviewCode) {
		t.Error("alert must not contain the review code")
	}
	if !strings.Contains(msgs[0], reviewPhone) {
		t.Errorf("alert should name the phone: %q", msgs[0])
	}
}

func TestReviewGuardCountsConcurrentFailuresExactly(t *testing.T) {
	var trips int
	var mu sync.Mutex
	g := &reviewGuard{}
	var wg sync.WaitGroup
	var accepted int
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, tripped := g.check("0000", reviewCode)
			mu.Lock()
			defer mu.Unlock()
			if ok {
				accepted++
			}
			if tripped {
				trips++
			}
		}()
	}
	wg.Wait()
	if trips != 1 {
		t.Fatalf("trips = %d, want 1", trips)
	}
	if accepted != 0 {
		t.Fatalf("accepted = %d", accepted)
	}
	if ok, disabled, _ := g.check(reviewCode, reviewCode); ok || !disabled {
		t.Fatalf("after lockout: ok=%v disabled=%v", ok, disabled)
	}
	if g.failures != reviewMaxFailures {
		t.Fatalf("failures = %d, want %d (no comparisons after lockout)", g.failures, reviewMaxFailures)
	}
}
