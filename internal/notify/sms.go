package notify

import (
	"context"
	"log/slog"
)

// SMSSender sends a plain (non-OTP) text message — used as the fallback
// channel for order-status updates when a customer has no device to push
// to (see notifications.Config.SMSFallback, env SMS_STATUS_FALLBACK).
//
// No real implementation yet: the only Nikita SMSPro API documented for
// this project (smspro.nikita.kg-OTP-api.pdf, tech spec §5) is the OTP
// API (/api/otp/send, /api/otp/verify), where Nikita generates the code
// text itself — it cannot carry arbitrary text. Nikita's separate
// bulk-message API needs its own credentials (login/password and a
// sender name registered with the operators), which the project doesn't
// have. Once they exist, a NikitaSMSClient implementing this interface is
// the only change needed; the dispatcher side is already wired.
type SMSSender interface {
	SendSMS(ctx context.Context, phone, text string) error
}

// NopSMSSender only logs — the SMSSender used until a real plain-SMS
// provider is configured.
type NopSMSSender struct{}

func (NopSMSSender) SendSMS(_ context.Context, phone, text string) error {
	slog.Info("notify: plain SMS provider not configured, SMS not sent",
		"phone_suffix", phoneSuffix(phone), "len", len([]rune(text)))
	return nil
}

// phoneSuffix keeps full phone numbers out of logs.
func phoneSuffix(phone string) string {
	if len(phone) <= 4 {
		return phone
	}
	return "…" + phone[len(phone)-4:]
}
