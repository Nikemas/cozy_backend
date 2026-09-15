// Package notify sends outbound notifications (SMS OTP, push) through
// third-party providers.
package notify

import "context"

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
