//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/notify"
)

// The store-review account logs in through the real OTP tables with its
// configured code, while the provider (mock, code 0000) is never consulted
// for that phone.
func TestReviewLoginAgainstRealDB(t *testing.T) {
	ctx := context.Background()
	phone := uniquePhone()
	review := config.ReviewLogin{Phone: phone, Code: "7391"}
	svc := auth.NewService(testDB, notify.NewMockClient(), []byte("0123456789abcdef0123456789abcdef"), config.AuthLimits{
		OTPPerIPPerHour: 100, OTPPerDay: 1000, OTPVerifyMaxAttempts: 5, OTPVerifyFailsPerIPPerHour: 100, RefreshPerIPPerMinute: 100,
	}, review)

	if err := svc.RequestOTP(ctx, phone); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.VerifyOTP(ctx, phone, "0000"); err == nil {
		t.Fatal("provider code must not log in the review phone")
	}
	if _, _, cust, err := svc.VerifyOTP(ctx, phone, "7391"); err != nil || cust.Phone != phone {
		t.Fatalf("review login: cust=%+v err=%v", cust, err)
	}

	other := uniquePhone()
	if err := svc.RequestOTP(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.VerifyOTP(ctx, other, "7391"); err == nil {
		t.Fatal("review code must not log in another phone")
	}
}
