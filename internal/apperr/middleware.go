package apperr

import (
	"encoding/json"
	"errors"
	"html"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/Nikemas/cozy_backend/internal/reqid"
)

// HandlerFunc is like http.HandlerFunc but allows returning an error, so
// business logic errors flow straight to a single translation point below.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// RequestID echoes the X-Request-ID of the failed request (set by
	// internal/httpmw.RequestID) so a user-reported error can be matched
	// to its server log lines. Omitted when no ID is in the context.
	RequestID string `json:"request_id,omitempty"`
}

// Wrap adapts a HandlerFunc into an http.HandlerFunc, converting any
// *AppError into a JSON error response and logging unexpected errors.
func Wrap(fn HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			WriteError(w, r, err)
		}
	}
}

// HTMLRenderer renders appErr as an HTML page (or an HTMX fragment) for a
// non-API request. It returns false to decline — e.g. the storefront's
// renderer declines /admin/* paths — in which case WriteError falls back
// to a minimal built-in HTML page.
type HTMLRenderer func(w http.ResponseWriter, r *http.Request, appErr *AppError) bool

var htmlRenderer atomic.Pointer[HTMLRenderer]

// SetHTMLRenderer installs fn as the HTML error renderer for every
// non-API path (see IsAPIPath). internal/web registers the storefront's
// branded 404/500 page here at startup. Passing nil removes it.
func SetHTMLRenderer(fn HTMLRenderer) {
	if fn == nil {
		htmlRenderer.Store(nil)
		return
	}
	htmlRenderer.Store(&fn)
}

// IsAPIPath reports whether path belongs to a JSON API (the mobile/public
// API under /api/ or the admin panel's XHR API under /admin/api/). Errors
// there stay JSON; everything else is a browser-facing page and gets HTML.
func IsAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/admin/api/")
}

// WriteError writes err as the standard JSON error body for API paths —
// *AppError as-is, anything else as a generic 500 — or as an HTML error
// page for browser-facing paths (the storefront, admin pages), logging
// 5xx errors with the request's context (and so its request_id).
// Exported for middleware that must produce the same shape outside Wrap,
// e.g. internal/httpmw.Recover.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var appErr *AppError
	if !errors.As(err, &appErr) {
		appErr = Internal(err)
	}

	if appErr.Status >= http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "code", appErr.Code, "err", err)
	}

	if !IsAPIPath(r.URL.Path) {
		writeHTMLError(w, r, appErr)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(appErr.Status)
	_ = json.NewEncoder(w).Encode(errorBody{
		Code:      appErr.Code,
		Message:   appErr.Message,
		RequestID: reqid.FromContext(r.Context()),
	})
}

// writeHTMLError renders appErr via the registered HTMLRenderer, falling
// back to a minimal self-contained page when none is set or it declines.
func writeHTMLError(w http.ResponseWriter, r *http.Request, appErr *AppError) {
	if fn := htmlRenderer.Load(); fn != nil && (*fn)(w, r, appErr) {
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(appErr.Status)
	status := strconv.Itoa(appErr.Status)
	_, _ = w.Write([]byte(`<!doctype html><html lang="ru"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex">` +
		`<title>` + status + `</title></head><body style="font-family:system-ui,sans-serif;padding:40px">` +
		`<h1>` + status + `</h1><p>` + html.EscapeString(appErr.Message) + `</p><p><a href="/">Cozy</a></p></body></html>`))
}
