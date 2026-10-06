package staff

import (
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/phonenorm"
)

// phoneLookupSuffixLen is how many trailing digits the tolerant staff
// lookup pre-filters on in SQL: the 9-digit subscriber number, which every
// spelling of a Kyrgyz number (+996XXXXXXXXX, 996…, 0XXXXXXXXX, with
// spaces/dashes) ends with.
const phoneLookupSuffixLen = 9

// normalizeStaffPhone returns the canonical +996XXXXXXXXX form staff phones
// are stored in from now on — the same rule customers' phones follow
// (internal/phonenorm). Used when an account is created, so an invalid
// number is rejected with apperr "invalid_phone".
func normalizeStaffPhone(raw string) (string, error) {
	p, ok := phonenorm.E164(raw)
	if !ok {
		return "", apperr.BadRequest("invalid_phone", "укажите номер Кыргызстана, например 0700 123 456 или +996 700 123 456")
	}
	return p, nil
}

// canonicalPhoneKey is the lenient variant used for login lookups and the
// login rate-limit key: a valid Kyrgyz number in any spelling becomes
// +996XXXXXXXXX; anything else (a legacy row stored in some other format
// before normalization existed, or a typo) is only trimmed, so it can still
// match a row stored exactly that way.
func canonicalPhoneKey(raw string) string {
	if p, ok := phonenorm.E164(raw); ok {
		return p
	}
	return strings.TrimSpace(raw)
}

// phoneDigitsSuffix returns the last phoneLookupSuffixLen digits of phone
// (all of them if there are fewer) — the SQL pre-filter value matched
// against the last 9 digits of the stored phone in GetByPhone's query.
func phoneDigitsSuffix(phone string) string {
	var b strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if len(digits) > phoneLookupSuffixLen {
		return digits[len(digits)-phoneLookupSuffixLen:]
	}
	return digits
}
