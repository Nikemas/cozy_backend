// Package notify sends outbound notifications (SMS OTP, push) through
// third-party providers.
package notify

import "context"

// Channels a login code can arrive through, shown to the user ("код
// отправлен в Telegram" vs "по SMS").
const (
	OTPChannelSMS      = "sms"
	OTPChannelTelegram = "telegram"
)

// ChannelOf reports the channel sender delivers codes through: senders
// that aren't SMS implement Channel(); everything else is SMS.
func ChannelOf(sender OTPSender) string {
	if c, ok := sender.(interface{ Channel() string }); ok {
		return c.Channel()
	}
	return OTPChannelSMS
}

// OTPSender abstracts the OTP provider so auth doesn't depend on Nikita
// SMSPro directly — swapping providers later means a new implementation
// here, nothing changes in internal/auth.
type OTPSender interface {
	// SendCode asks the provider to text a one-time code to phone and
	// returns the provider's transaction token, used later to verify it.
	SendCode(ctx context.Context, phone, transactionID string) (token string, err error)

	// VerifyCode checks a user-entered code against the given transaction
	// token. Returns nil if the code is correct.
	VerifyCode(ctx context.Context, token, code string) error
}
