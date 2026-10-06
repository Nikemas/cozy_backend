// Package phonenorm holds the single definition of a canonical customer
// phone number, shared by internal/auth (request input) and
// internal/config (startup validation) which cannot import each other.
package phonenorm

import (
	"regexp"
	"strings"
)

var (
	localPhoneRe    = regexp.MustCompile(`^0\d{9}$`)
	intlPhoneRe     = regexp.MustCompile(`^996\d{9}$`)
	nationalPhoneRe = regexp.MustCompile(`^[1-9]\d{8}$`)
	nonDigitRe      = regexp.MustCompile(`\D`)
)

// E164 accepts a Kyrgyz number written as +996XXXXXXXXX, 996XXXXXXXXX, a
// local 0XXXXXXXXX or a bare 9-digit national XXXXXXXXX (spaces, dashes,
// parentheses and other separators are ignored) and returns the canonical
// +996XXXXXXXXX form stored in the database.
//
// A bare 9-digit number written with a leading "+" is rejected: "+" marks
// an international number, and without 996 it is not a Kyrgyz one.
func E164(raw string) (string, bool) {
	digits := nonDigitRe.ReplaceAllString(raw, "")
	switch {
	case intlPhoneRe.MatchString(digits):
		return "+" + digits, true
	case localPhoneRe.MatchString(digits):
		return "+996" + digits[1:], true
	case nationalPhoneRe.MatchString(digits) && !strings.HasPrefix(strings.TrimSpace(raw), "+"):
		return "+996" + digits, true
	default:
		return "", false
	}
}
