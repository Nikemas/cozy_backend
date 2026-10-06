// Package phonenorm holds the single definition of a canonical customer
// phone number, shared by internal/auth (request input) and
// internal/config (startup validation) which cannot import each other.
package phonenorm

import "regexp"

var (
	localPhoneRe = regexp.MustCompile(`^0\d{9}$`)
	intlPhoneRe  = regexp.MustCompile(`^996\d{9}$`)
	nonDigitRe   = regexp.MustCompile(`\D`)
)

// E164 accepts +996XXXXXXXXX, 996XXXXXXXXX or a local 0XXXXXXXXX number and
// returns the canonical +996XXXXXXXXX form stored in the database.
func E164(raw string) (string, bool) {
	digits := nonDigitRe.ReplaceAllString(raw, "")
	switch {
	case intlPhoneRe.MatchString(digits):
		return "+" + digits, true
	case localPhoneRe.MatchString(digits):
		return "+996" + digits[1:], true
	default:
		return "", false
	}
}
