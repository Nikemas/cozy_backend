package orders

import (
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/phonenorm"
)

// normalizeCourierPhone validates the contact number the courier calls
// (orders.courier_phone, migration 000048) — it may differ from the
// account's phone (a gift, a relative picks it up at the door).
//
// Pickup orders never carry one: the value is ignored. For delivery a blank
// value is stored as NULL (older app builds don't send it; staff then call
// the account phone); anything else must be a Kyrgyz number and is stored
// in the canonical +996XXXXXXXXX form.
func normalizeCourierPhone(raw string, delivery bool) (*string, error) {
	if !delivery || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	phone, ok := phonenorm.E164(raw)
	if !ok {
		return nil, apperr.BadRequest("invalid_courier_phone",
			"укажите номер для курьера — номер Кыргызстана, например 0700 123 456 или +996 700 123 456")
	}
	return &phone, nil
}
