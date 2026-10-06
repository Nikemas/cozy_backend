// toast.go (fix/admin-ux-followups): the one-shot message a screen shows
// after a redirect (Товары, Заказы, Остатки, Точки, Категории). The URL
// carries only a key from adminToasts plus validated parameters — never
// text — so a crafted link can't make the panel show staff an arbitrary
// message (phishing). Unknown keys or bad parameters show nothing.
package admin

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// Query params of a toast. Every one starts with toastParam so
// stripOneShotParams can drop them all.
const (
	toastParam       = "toast"
	toastStatusParam = "toast_st"
	toastNParam      = "toast_n"
	toastOfParam     = "toast_of"
	toastCodeParam   = "toast_code"

	// toastErrorKey is the generic error toast: its message is the locale
	// text of an apperr code (admin.apperr.<code>), or admin.err.generic.
	toastErrorKey = "error"
)

// maxToastCount bounds the counters a toast may carry (bulk results,
// saved stock positions) — far above any real value, it only keeps a
// crafted link from showing absurd numbers.
const maxToastCount = 100_000

// apperrCodePattern is what an apperr code looks like (lowercase snake).
var apperrCodePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// flash is one toast as it travels through a redirect URL.
type flash struct {
	Key    string
	Status orders.OrderStatus
	N, Of  int
	Code   string
	hasN   bool
}

func toastKey(key string) flash { return flash{Key: key} }

func toastOrderStatus(s orders.OrderStatus) flash {
	return flash{Key: "order_status", Status: s}
}

func toastCount(key string, n int) flash { return flash{Key: key, N: n, hasN: true} }

func toastNOf(key string, n, of int) flash {
	return flash{Key: key, N: n, Of: of, hasN: true}
}

// toastForErr is the error toast for a service error: its apperr code
// (a known locale key on display) or the generic message.
func toastForErr(err error) flash {
	f := flash{Key: toastErrorKey}
	var ae *apperr.AppError
	if errors.As(err, &ae) && apperrCodePattern.MatchString(ae.Code) {
		f.Code = ae.Code
	}
	return f
}

// values encodes f as query params.
func (f flash) values() url.Values {
	v := url.Values{}
	if f.Key == "" {
		return v
	}
	v.Set(toastParam, f.Key)
	if f.Status != "" {
		v.Set(toastStatusParam, string(f.Status))
	}
	if f.hasN {
		v.Set(toastNParam, strconv.Itoa(f.N))
	}
	if f.Of > 0 {
		v.Set(toastOfParam, strconv.Itoa(f.Of))
	}
	if f.Code != "" {
		v.Set(toastCodeParam, f.Code)
	}
	return v
}

// withToast appends f's params to path (which may already have a query).
func withToast(path string, f flash) string {
	v := f.values()
	if len(v) == 0 {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + v.Encode()
}

// redirectWithToast redirects to path with the toast f, or — for a
// request HTMX issued (the delete confirm modal's hx-post) — sets
// HX-Redirect instead so htmx performs a full client-side navigation
// rather than trying to swap the redirect's HTML into whatever element
// triggered it, mirroring internal/web's redirectToLogin.
func redirectWithToast(w http.ResponseWriter, r *http.Request, path string, f flash) {
	target := withToast(path, f)
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// toastRenderer turns a parsed flash into its message ("" = not shown).
type toastRenderer func(t tr, f flash) string

func fixedToast(localeKey string) toastRenderer {
	return func(t tr, _ flash) string { return t.T(localeKey) }
}

// adminToasts is the closed set of toasts a URL can ask for.
var adminToasts = map[string]toastRenderer{
	"product_saved":        fixedToast("admin.product.toast_saved"),
	"product_activated":    fixedToast("admin.product.toast_activated"),
	"product_deactivated":  fixedToast("admin.product.toast_deactivated"),
	"product_deleted":      fixedToast("admin.product.toast_deleted"),
	"point_saved":          fixedToast("admin.points.toast_saved"),
	"point_activated":      fixedToast("admin.points.toast_activated"),
	"point_deactivated":    fixedToast("admin.points.toast_deactivated"),
	"category_saved":       fixedToast("admin.categories.toast_saved"),
	"category_deleted":     fixedToast("admin.categories.toast_deleted"),
	"stock_no_changes":     fixedToast("admin.stock.no_changes"),
	"form_error":           fixedToast("admin.err.form"),
	"bulk_none":            fixedToast("admin.bulk.none_selected"),
	"bulk_choose_status":   fixedToast("admin.bulk.choose_status"),
	"bulk_unknown_action":  fixedToast("admin.bulk.unknown_action"),
	"cancel_forbidden":     fixedToast("admin.apperr.cancel_forbidden"),
	"invalid_category":     fixedToast("admin.apperr.invalid_category_id"),
	"status_change_failed": fixedToast("admin.order.status_change_failed"),
	"bulk_too_many": func(t tr, _ flash) string {
		return t.F("admin.bulk.too_many", maxBulkItems)
	},
	"stock_saved": func(t tr, f flash) string {
		if !f.hasN || f.N < 1 {
			return ""
		}
		return t.F("admin.stock.saved", t.N(f.N, "admin.plural.position"))
	},
	"order_status": func(t tr, f flash) string {
		if !isKnownOrderStatus(f.Status) {
			return ""
		}
		return t.F("admin.order.status_changed", orderStatusMetaFor(t, f.Status).Label)
	},
	"bulk_status": func(t tr, f flash) string {
		if !isKnownOrderStatus(f.Status) || !validNOf(f) {
			return ""
		}
		return t.F("admin.bulk.status_done", orderStatusMetaFor(t, f.Status).Label, f.N, f.Of)
	},
	"products_bulk": func(t tr, f flash) string {
		if !validNOf(f) {
			return ""
		}
		return t.F("admin.bulk.products_done", f.N, f.Of, t.Plural(f.Of, "admin.plural.product_gen"))
	},
	toastErrorKey: func(t tr, f flash) string { return apperrCodeMessage(t, f.Code) },
}

func validNOf(f flash) bool {
	return f.hasN && f.N >= 0 && f.Of >= 1 && f.N <= f.Of && f.Of <= maxBulkItems
}

// isKnownOrderStatus: s is one of the order state machine's statuses.
func isKnownOrderStatus(s orders.OrderStatus) bool {
	for _, f := range orderStatusFilters {
		if f.Value != "" && f.Value == string(s) {
			return true
		}
	}
	return false
}

// apperrCodeMessage is the locale text for an apperr code when the
// bundle has one, else the generic error — never the code itself.
func apperrCodeMessage(t tr, code string) string {
	if apperrCodePattern.MatchString(code) {
		key := "admin.apperr." + code
		if adminBundle.Has(t.Lang(), key) {
			return t.T(key)
		}
	}
	return t.T("admin.err.generic")
}

// parseToastCount reads a bounded non-negative counter.
func parseToastCount(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > maxToastCount {
		return 0, false
	}
	return n, true
}

// toastFromQuery renders the toast q asks for, or "" when the key is
// unknown or its parameters don't validate.
func toastFromQuery(t tr, q url.Values) string {
	render, ok := adminToasts[q.Get(toastParam)]
	if !ok {
		return ""
	}
	f := flash{
		Key:    q.Get(toastParam),
		Status: orders.OrderStatus(q.Get(toastStatusParam)),
		Code:   q.Get(toastCodeParam),
	}
	if s := q.Get(toastNParam); s != "" {
		n, ok := parseToastCount(s)
		if !ok {
			return ""
		}
		f.N, f.hasN = n, true
	}
	if s := q.Get(toastOfParam); s != "" {
		of, ok := parseToastCount(s)
		if !ok {
			return ""
		}
		f.Of = of
	}
	return render(t, f)
}

// pageToast is the toast of the request's URL in the viewer's language.
func (h *handlers) pageToast(r *http.Request) string {
	return toastFromQuery(h.tr(r), r.URL.Query())
}

// oneShotParam reports whether a query param belongs to a one-shot
// message (a toast or a bulk result) that must not follow the user to
// another page or survive a "back" link.
func oneShotParam(name string) bool {
	return strings.HasPrefix(name, toastParam) || name == bulkFailParam || name == bulkMoreParam || name == legacyStatusErrorParam
}

// legacyStatusErrorParam carried free text before toast keys; it is no
// longer read, only dropped from links.
const legacyStatusErrorParam = "status_error"

// stripOneShotParams returns a copy of q without toast/bulk-result params.
func stripOneShotParams(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		if oneShotParam(k) {
			continue
		}
		out[k] = append([]string(nil), v...)
	}
	return out
}

// returnURL is the request's own path and query minus one-shot params —
// what a form on the page sends back as "back" so the redirect returns to
// the same filtered list without replaying an old toast.
func returnURL(r *http.Request) string {
	q := stripOneShotParams(r.URL.Query())
	if len(q) == 0 {
		return r.URL.Path
	}
	return r.URL.Path + "?" + q.Encode()
}
