package auth

import (
	"regexp"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

var localPhoneRe = regexp.MustCompile(`^0\d{9}$`)
var intlPhoneRe = regexp.MustCompile(`^996\d{9}$`)
var digitsOnlyRe = regexp.MustCompile(`\D`)

// NormalizePhone accepts +996XXXXXXXXX, 996XXXXXXXXX or a local 0XXXXXXXXX
// number and returns it in the canonical +996XXXXXXXXX form stored in the
// database. Client-side validation exists too, but the server is the
// source of truth (§5 ТЗ).
func NormalizePhone(raw string) (string, error) {
	digits := digitsOnlyRe.ReplaceAllString(raw, "")

	switch {
	case intlPhoneRe.MatchString(digits):
		return "+" + digits, nil
	case localPhoneRe.MatchString(digits):
		return "+996" + digits[1:], nil
	default:
		return "", apperr.BadRequest("invalid_phone", "укажите номер в формате +996XXXXXXXXX")
	}
}

// forNikita strips the leading '+' — Nikita SMSPro expects the phone
// without it (see smspro.nikita.kg-OTP-api.pdf example: "996770123456").
func forNikita(e164Phone string) string {
	return e164Phone[1:]
}
