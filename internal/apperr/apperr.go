// Package apperr defines the single error type business logic must return.
package apperr

import "net/http"

// AppError is the only error type business logic layers should return.
// Handlers translate it directly into a JSON response.
type AppError struct {
	Status  int    // HTTP status code
	Code    string // machine-readable code, e.g. "insufficient_stock"
	Message string // human-readable message
}

func (e *AppError) Error() string {
	return e.Message
}

func New(status int, code, message string) *AppError {
	return &AppError{Status: status, Code: code, Message: message}
}

func NotFound(code, message string) *AppError {
	return New(http.StatusNotFound, code, message)
}

func BadRequest(code, message string) *AppError {
	return New(http.StatusBadRequest, code, message)
}

func Unauthorized(code, message string) *AppError {
	return New(http.StatusUnauthorized, code, message)
}

func Forbidden(code, message string) *AppError {
	return New(http.StatusForbidden, code, message)
}

func Conflict(code, message string) *AppError {
	return New(http.StatusConflict, code, message)
}

// Internal wraps err as a 500 AppError. err.Error() may contain SQL
// fragments, driver internals, or other details that must not leak to
// unauthenticated API callers, so the client-facing Message is generic;
// middleware.Wrap logs the original err separately.
func Internal(err error) *AppError {
	return &AppError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "internal server error"}
}
