package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// mockOTPCode is the only code MockClient ever accepts — matches the
// demo-code used in the design prototype (COZY_WEB_DESIGN.md §3.5),
// which is explicitly NOT how the real backend works per the tech spec's
// auth section. Local dev/demo only — see config.Config.SMSMockOTP.
const mockOTPCode = "0000"

// MockClient implements OTPSender without talking to Nikita or sending
// any real SMS: SendCode always succeeds (no phone validation beyond
// what internal/auth already does), VerifyCode accepts exactly
// mockOTPCode. Wired in only when cfg.SMSMockOTP is explicitly set and
// Env != "prod" — see cmd/server/main.go.
type MockClient struct{}

func NewMockClient() *MockClient { return &MockClient{} }

func (c *MockClient) SendCode(ctx context.Context, phone, transactionID string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	slog.Warn("notify: MOCK SMS (dev only) — code is always 0000, no SMS actually sent", "phone", phone)
	return token, nil
}

func (c *MockClient) VerifyCode(ctx context.Context, token, code string) error {
	if code != mockOTPCode {
		return apperr.New(http.StatusBadRequest, "otp_invalid_code", "неверный код (в dev-режиме используйте 0000)")
	}
	return nil
}

func randomToken() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
