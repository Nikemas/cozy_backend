package web

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// ErrorPageData backs error.gohtml — the storefront's branded 404/500
// page (and every other status a site handler can fail with).
type ErrorPageData struct {
	Status   int
	TitleKey string // i18n key for the heading
	Message  string // already translated
	// ShowLogin adds a "войти" CTA — a 401 on a page means the visitor
	// needs to sign in, not that something broke.
	ShowLogin bool
}

// notFound is the catch-all "/" handler: any URL no other route matched
// gets the branded 404 page instead of net/http's plain-text
// "404 page not found". Unknown /api/ URLs still get the JSON error body
// (apperr.WriteError decides by path).
func (h *handlers) notFound(http.ResponseWriter, *http.Request) error {
	return apperr.NotFound("not_found", "страница не найдена")
}

// renderHTMLError is registered as apperr's HTML renderer (see
// RegisterRoutes). For a plain browser request it renders error.gohtml
// with the right status code; for an HTMX request it retargets the
// response into the toast slot (HX-Retarget/HX-Reswap — layout.gohtml's
// htmx:beforeSwap hook lets htmx swap a 4xx/5xx body when those headers
// are present), so a failed hx-post shows a readable toast instead of
// failing silently. /admin/* paths are declined — the admin panel has its
// own chrome, and apperr falls back to a minimal page there.
func (h *handlers) renderHTMLError(w http.ResponseWriter, r *http.Request, appErr *apperr.AppError) bool {
	if strings.HasPrefix(r.URL.Path, "/admin") {
		return false
	}
	lang := h.resolveLang(r)
	msg := h.errorText(lang, appErr)

	if isHX(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("HX-Retarget", "#toast-slot")
		w.Header().Set("HX-Reswap", "innerHTML")
		w.WriteHeader(appErr.Status)
		_, _ = w.Write([]byte(errorToastHTML(msg)))
		return true
	}

	data := h.base(r, "error")
	data.NoIndex = true
	pd := ErrorPageData{Status: appErr.Status, Message: msg, TitleKey: "error.generic.title"}
	switch {
	case appErr.Status == http.StatusNotFound:
		pd.TitleKey = "error.not_found.title"
	case appErr.Status == http.StatusUnauthorized:
		pd.TitleKey = "error.unauthorized.title"
		pd.ShowLogin = true
	case appErr.Status >= http.StatusInternalServerError:
		pd.TitleKey = "error.internal.title"
	}
	data.Data = pd
	if err := h.render.RenderStatus(w, appErr.Status, "error", data); err != nil {
		slog.ErrorContext(r.Context(), "web: rendering error page failed", "err", err)
		return false
	}
	return true
}

// errorText returns a customer-facing, translated message for appErr:
// a per-code locale key ("error.code.<code>") when one exists, else a
// generic per-status message. Internal error text never reaches the page.
func (h *handlers) errorText(lang string, appErr *apperr.AppError) string {
	if appErr.Code != "" {
		key := "error.code." + appErr.Code
		if v := h.t(lang, key); v != key {
			return v
		}
	}
	switch {
	case appErr.Status == http.StatusNotFound:
		return h.t(lang, "error.not_found.text")
	case appErr.Status == http.StatusUnauthorized:
		return h.t(lang, "error.unauthorized.text")
	case appErr.Status == http.StatusTooManyRequests:
		return h.t(lang, "error.code.rate_limited")
	case appErr.Status >= http.StatusInternalServerError:
		return h.t(lang, "error.internal.text")
	}
	// A 4xx whose code has no translation yet: the domain message is
	// Russian and meant for customers, so show it on the RU site; the KY
	// site gets the generic text rather than a Russian sentence.
	if lang == "ru" && appErr.Message != "" {
		return appErr.Message
	}
	return h.t(lang, "error.generic.text")
}

// errText is errorText for an arbitrary error (a non-AppError is treated
// as an internal error and never shown verbatim).
func (h *handlers) errText(lang string, err error) string {
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		appErr = apperr.Internal(err)
	}
	return h.errorText(lang, appErr)
}

// errorToastHTML is the toast markup for an error message (same shape as
// _toast_inner, with a warning icon).
func errorToastHTML(msg string) string {
	return `<div class="toast toast--error" role="alert"><i class="ti ti-alert-circle" aria-hidden="true"></i> ` +
		template.HTMLEscapeString(msg) + `</div>`
}
