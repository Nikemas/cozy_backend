package apperr

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

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

// WriteError writes err as the standard JSON error body — *AppError as-is,
// anything else as a generic 500 — logging 5xx errors with the request's
// context (and so its request_id). Exported for middleware that must
// produce the same shape outside Wrap, e.g. internal/httpmw.Recover.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var appErr *AppError
	if !errors.As(err, &appErr) {
		appErr = Internal(err)
	}

	if appErr.Status >= http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "code", appErr.Code, "err", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(appErr.Status)
	_ = json.NewEncoder(w).Encode(errorBody{
		Code:      appErr.Code,
		Message:   appErr.Message,
		RequestID: reqid.FromContext(r.Context()),
	})
}
