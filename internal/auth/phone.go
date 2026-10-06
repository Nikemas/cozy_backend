package auth

import (
	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/phonenorm"
)

// NormalizePhone accepts +996XXXXXXXXX, 996XXXXXXXXX, a local 0XXXXXXXXX or
// a bare 9-digit XXXXXXXXX number (separators ignored, see phonenorm.E164)
// and returns it in the canonical +996XXXXXXXXX form stored in the
// database. Client-side validation exists too, but the server is the
// source of truth (§5 ТЗ).
func NormalizePhone(raw string) (string, error) {
	phone, ok := phonenorm.E164(raw)
	if !ok {
		return "", apperr.BadRequest("invalid_phone", "укажите номер Кыргызстана, например 0700 123 456 или +996 700 123 456")
	}
	return phone, nil
}

// forNikita strips the leading '+' — Nikita SMSPro expects the phone
// without it (see smspro.nikita.kg-OTP-api.pdf example: "996770123456").
func forNikita(e164Phone string) string {
	return e164Phone[1:]
}
