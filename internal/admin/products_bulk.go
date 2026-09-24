package admin

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// bulkIDs reads the checked rows' ids (form field "id", repeated): valid
// uuids only, deduplicated, at most maxBulkItems. ok is false when nothing
// usable was selected or too much was.
func bulkIDs(values []string) (ids []string, errMsg string) {
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
		return nil, "Не выбрано ни одной строки"
	case len(ids) > maxBulkItems:
		return nil, fmt.Sprintf("За один раз можно изменить не больше %d строк", maxBulkItems)
	}
	return ids, ""
}

// safeReturnURL returns back if it is a same-site link to listPath (the
// list with its filters), else listPath — never an open redirect. Any
// stale toast/bulk-result params are dropped.
func safeReturnURL(back, listPath string) string {
	u, err := url.Parse(back)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path != listPath {
		return listPath
	}
	q := u.Query()
	q.Del("toast")
	q.Del("bulk_fail")
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
		redirectWithToast(w, r, "/admin/products", "не удалось прочитать форму")
		return
	}
	back := safeReturnURL(r.FormValue("back"), "/admin/products")

	ids, errMsg := bulkIDs(r.Form["id"])
	if errMsg != "" {
		redirectWithToast(w, r, back, errMsg)
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
			redirectWithToast(w, r, back, "Выберите категорию")
			return
		}
		changed, err = h.productOps.BulkSetCategory(ctx, ids, categoryID)
	default:
		redirectWithToast(w, r, back, "Неизвестное действие")
		return
	}
	if err != nil {
		var ae *apperr.AppError
		if !errors.As(err, &ae) {
			slog.ErrorContext(ctx, "admin: products bulk action failed", "action", r.FormValue("action"), "err", err)
		}
		redirectWithToast(w, r, back, appErrMessage(err))
		return
	}
	redirectWithToast(w, r, back, fmt.Sprintf("Готово: изменено %d из %d %s", changed, len(ids),
		pluralRu(len(ids), "товара", "товаров", "товаров")))
}
