package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

const (
	// accountDeletionPage is the public "how to delete your account" page
	// Google Play requires (reachable without installing the app).
	accountDeletionPage = "account-deletion"

	// deleteConfirmValue is what the confirmation checkbox on /profile
	// posts; anything else means the customer didn't confirm.
	deleteConfirmValue = "yes"

	// Values of /profile?delete=… explaining why deletion didn't happen.
	deleteStatusActiveOrders = "active_orders"
	deleteStatusUnconfirmed  = "unconfirmed"

	// codeHasActiveOrders / codeCustomerNotFound are the auth package's
	// AppError codes for "orders still in progress" and "no such customer".
	codeHasActiveOrders  = "has_active_orders"
	codeCustomerNotFound = "customer_not_found"
)

// customerDeleter is the subset of *auth.Service the site's account
// deletion needs — the same DeleteCustomer DELETE /api/v1/customer calls.
type customerDeleter interface {
	DeleteCustomer(ctx context.Context, customerID string) error
}

// accountDelete handles POST /account/delete from the profile screen: a
// guest is sent to the login screen; an unconfirmed form or a 409
// has_active_orders goes back to /profile with the reason; on success the
// session cookie is cleared and the customer lands on /account-deletion
// with a "deleted" notice. The CSRF same-origin check (internal/csrf)
// applies to this POST like to every other storefront mutation.
func (h *handlers) accountDelete(w http.ResponseWriter, r *http.Request) error {
	customerID := CustomerID(r)
	if customerID == "" {
		return h.redirectOrHXRedirect(w, r, "/profile")
	}
	if err := r.ParseForm(); err != nil {
		return apperr.BadRequest("bad_request", "некорректная форма")
	}
	if r.FormValue("confirm") != deleteConfirmValue {
		return h.redirectOrHXRedirect(w, r, "/profile?delete="+deleteStatusUnconfirmed+"#account-delete")
	}

	if err := h.accounts.DeleteCustomer(r.Context(), customerID); err != nil {
		var appErr *apperr.AppError
		if !errors.As(err, &appErr) {
			return err
		}
		switch appErr.Code {
		case codeHasActiveOrders:
			return h.redirectOrHXRedirect(w, r, "/profile?delete="+deleteStatusActiveOrders+"#account-delete")
		case codeCustomerNotFound:
			// Nothing left to delete: just end the stale session below.
		default:
			return err
		}
	}

	clearSessionCookie(w)
	return h.redirectOrHXRedirect(w, r, "/"+accountDeletionPage+"?deleted=1")
}

// deleteStatusText is the profile screen's message for ?delete=<status>,
// or "" when there is nothing to show.
func (h *handlers) deleteStatusText(lang, status string) string {
	switch status {
	case deleteStatusActiveOrders:
		return h.t(lang, "account_delete.has_active_orders")
	case deleteStatusUnconfirmed:
		return h.t(lang, "account_delete.unconfirmed")
	default:
		return ""
	}
}
