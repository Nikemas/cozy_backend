package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/phonenorm"
)

// OTPCodeLength is the number of digits in a customer OTP (the mock sender
// and the Nikita OTP service are both configured for 4-digit codes).
const OTPCodeLength = 4

// ReviewLogin is the opt-in store-review account: Apple App Review and
// Google Play reviewers cannot receive an SMS, so for exactly one phone the
// backend skips the SMS and accepts a fixed code through the normal OTP
// path (expiry, attempt and abuse limits still apply). Zero value =
// disabled.
type ReviewLogin struct {
	Phone string // canonical +996XXXXXXXXX
	Code  string // OTPCodeLength digits, never logged
}

// Enabled reports whether the review account is configured.
func (r ReviewLogin) Enabled() bool { return r.Phone != "" }

// loadReviewLogin reads REVIEW_PHONE / REVIEW_OTP_CODE. Both unset means
// disabled; exactly one set, a malformed phone or a weak code is a startup
// error. Error messages never echo the code.
func loadReviewLogin() (ReviewLogin, error) {
	rawPhone := strings.TrimSpace(os.Getenv("REVIEW_PHONE"))
	code := strings.TrimSpace(os.Getenv("REVIEW_OTP_CODE"))
	if rawPhone == "" && code == "" {
		return ReviewLogin{}, nil
	}
	if rawPhone == "" || code == "" {
		return ReviewLogin{}, errors.New("REVIEW_PHONE and REVIEW_OTP_CODE must be set together (or both left empty)")
	}
	phone, ok := phonenorm.E164(rawPhone)
	if !ok {
		return ReviewLogin{}, errors.New("REVIEW_PHONE must be a Kyrgyz number like +996XXXXXXXXX")
	}
	if err := validateReviewCode(code); err != nil {
		return ReviewLogin{}, fmt.Errorf("REVIEW_OTP_CODE %w", err)
	}
	return ReviewLogin{Phone: phone, Code: code}, nil
}

// weakCodes are the most common PINs that also pass the structural checks.
var weakCodes = map[string]bool{"2580": true, "1004": true, "2000": true, "1122": true, "6969": true}

func validateReviewCode(code string) error {
	if len(code) != OTPCodeLength {
		return fmt.Errorf("must be exactly %d digits", OTPCodeLength)
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return fmt.Errorf("must be exactly %d digits", OTPCodeLength)
		}
	}
	if isWeakCode(code) {
		return errors.New("is too easy to guess (repeated digits, sequence or common code) — pick a random one")
	}
	return nil
}

func isWeakCode(code string) bool {
	if weakCodes[code] {
		return true
	}
	allSame, up, down, period2 := true, true, true, true
	for i := 1; i < len(code); i++ {
		d := int(code[i]) - int(code[i-1])
		allSame = allSame && d == 0
		up = up && d == 1
		down = down && d == -1
		if i >= 2 {
			period2 = period2 && code[i] == code[i-2]
		}
	}
	return allSame || up || down || period2
}
