package auth

import (
	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/phonenorm"
)

// NormalizePhone accepts +996XXXXXXXXX, 996XXXXXXXXX or a local 0XXXXXXXXX
// number and returns it in the canonical +996XXXXXXXXX form stored in the
// database. Client-side validation exists too, but the server is the
// source of truth (§5 ТЗ).
func NormalizePhone(raw string) (string, error) {
	phone, ok := phonenorm.E164(raw)
	if !ok {
		return "", apperr.BadRequest("invalid_phone", "укажите номер в формате +996XXXXXXXXX")
	}
	return phone, nil
}

// forNikita strips the leading '+' — Nikita SMSPro expects the phone
// without it (see smspro.nikita.kg-OTP-api.pdf example: "996770123456").
func forNikita(e164Phone string) string {
	return e164Phone[1:]
}
