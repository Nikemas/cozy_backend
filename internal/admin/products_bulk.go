package admin

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// bulkIDs reads the checked rows' ids (form field "id", repeated): valid
// uuids only, deduplicated, at most maxBulkItems. ok is false when nothing
// usable was selected or too much was (errToast says which).
func bulkIDs(values []string) (ids []string, errToast flash) {
	seen := map[string]bool{}
	for _, v := range values {
		id, err := uuid.Parse(strings.TrimSpace(v))
		if err != nil {
			continue
		}
		s := id.String()
		if !seen[s] {
			seen[s] = true
			ids = append(ids, s)
		}
	}
	switch {
	case len(ids) == 0:
		return nil, toastKey("bulk_none")
	case len(ids) > maxBulkItems:
		return nil, toastKey("bulk_too_many")
	}
	return ids, flash{}
}

// safeReturnURL returns back if it is a same-site link to listPath (the
// list with its filters), else listPath — never an open redirect. Any
// stale toast/bulk-result params are dropped.
func safeReturnURL(back, listPath string) string {
	u, err := url.Parse(back)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path != listPath {
		return listPath
	}
	q := stripOneShotParams(u.Query())
	if len(q) == 0 {
		return listPath
	}
	return listPath + "?" + q.Encode()
}

// productsBulk handles POST /admin/products/bulk — the products list's
// bulk bar (after the shared confirm dialog): action=activate|deactivate
// |category (+ category_id) for every checked row.
func (h *handlers) productsBulk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		redirectWithToast(w, r, "/admin/products", toastKey("form_error"))
		return
	}
	back := safeReturnURL(r.FormValue("back"), "/admin/products")

	ids, errToast := bulkIDs(r.Form["id"])
	if errToast.Key != "" {
		redirectWithToast(w, r, back, errToast)
		return
	}

	var changed int
	var err error
	switch r.FormValue("action") {
	case "activate":
		changed, err = h.productOps.BulkSetActive(ctx, ids, true)
	case "deactivate":
		changed, err = h.productOps.BulkSetActive(ctx, ids, false)
	case "category":
		categoryID := strings.TrimSpace(r.FormValue("category_id"))
		if _, perr := uuid.Parse(categoryID); perr != nil {
			redirectWithToast(w, r, back, toastKey("invalid_category"))
			return
		}
		changed, err = h.productOps.BulkSetCategory(ctx, ids, categoryID)
	default:
		redirectWithToast(w, r, back, toastKey("bulk_unknown_action"))
		return
	}
	if err != nil {
		var ae *apperr.AppError
		if !errors.As(err, &ae) {
			slog.ErrorContext(ctx, "admin: products bulk action failed", "action", r.FormValue("action"), "err", err)
		}
		redirectWithToast(w, r, back, toastForErr(err))
		return
	}
	redirectWithToast(w, r, back, toastNOf("products_bulk", changed, len(ids)))
}
