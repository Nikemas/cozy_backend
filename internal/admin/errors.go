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
// Any other error (a raw database/driver failure) gets a generic message
// instead of leaking internals to the page.
func appErrMessage(t tr, err error) string {
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		key := "admin.apperr." + ae.Code
		if t.Lang() != i18n.LangRU && adminBundle.Has(t.Lang(), key) {
			return t.T(key)
		}
		return ae.Message
	}
	return t.T("admin.err.generic")
}
