package apperr

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// HandlerFunc is like http.HandlerFunc but allows returning an error, so
// business logic errors flow straight to a single translation point below.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Wrap adapts a HandlerFunc into an http.HandlerFunc, converting any
// *AppError into a JSON error response and logging unexpected errors.
func Wrap(fn HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := fn(w, r)
		if err == nil {
			return
		}

		var appErr *AppError
		if !errors.As(err, &appErr) {
			appErr = Internal(err)
		}

		if appErr.Status >= http.StatusInternalServerError {
			slog.Error("request failed", "path", r.URL.Path, "code", appErr.Code, "err", err)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(appErr.Status)
		_ = json.NewEncoder(w).Encode(errorBody{Code: appErr.Code, Message: appErr.Message})
	}
}
