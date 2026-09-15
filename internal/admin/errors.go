package admin

import (
	"errors"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// appErrMessage extracts the human-readable (already Russian) message from
// an *apperr.AppError so a form handler can surface it on the page — e.g.
// staff.Service.UpdateStaff's apperr.Conflict("last_owner", "нельзя
// понизить или деактивировать последнего владельца"). Any other error
// (a raw database/driver failure) gets a generic message instead of
// leaking internals to the page.
func appErrMessage(err error) string {
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		return ae.Message
	}
	return "произошла ошибка, попробуйте ещё раз"
}
