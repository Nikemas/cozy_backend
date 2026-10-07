package admin

import (
	"errors"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// appErrMessage extracts the human-readable message from an
// *apperr.AppError so a form handler can surface it on the page — e.g.
// staff.Service.UpdateStaff's apperr.Conflict("last_owner", "нельзя
// понизить или деактивировать последнего владельца"). Services write that
// message in Russian, which is what the Russian admin shows verbatim; in
// Kyrgyz the error's Code picks admin.apperr.<code> from the locale file
// (falling back to the Russian message for a code with no translation).
// A localizedError (admin-side validation) is shown in t's language.
// Any other error (a raw database/driver failure) gets a generic message
// instead of leaking internals to the page. The message is shown on its
// own, so it starts with a capital letter (services write theirs in
// lowercase — they are also composed mid-sentence).
func appErrMessage(t tr, err error) string {
	var le localizedError
	if errors.As(err, &le) {
		return i18n.UpperFirst(t.T(le.key))
	}
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		key := "admin.apperr." + ae.Code
		if t.Lang() != i18n.LangRU && adminBundle.Has(t.Lang(), key) {
			return i18n.UpperFirst(t.T(key))
		}
		return i18n.UpperFirst(ae.Message)
	}
	return t.T("admin.err.generic")
}
