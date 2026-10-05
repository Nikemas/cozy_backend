// Package apperr defines the single error type business logic must return.
package apperr

import "net/http"

// AppError is the only error type business logic layers should return.
// Handlers translate it directly into a JSON response.
type AppError struct {
	Status  int    // HTTP status code
	Code    string // machine-readable code, e.g. "insufficient_stock"
	Message string // human-readable message (Russian; see Localize)

	// variant narrows the translation key when one Code is raised with
	// several different messages (see MessageKey); "" = the code's
	// default message.
	variant string
	// params fill {name} placeholders in the translated message — the
	// values a Russian Message bakes in with fmt.Sprintf (product name,
	// remaining stock, ...).
	params map[string]string
}

func (e *AppError) Error() string {
	return e.Message
}

// WithVariant returns a copy of e whose translation key is
// "err.<code>.<variant>" instead of "err.<code>" — for a code raised with
// more than one distinct message. Code and Status are unchanged.
func (e *AppError) WithVariant(variant string) *AppError {
	c := *e
	c.variant = variant
	return &c
}

// WithParams returns a copy of e carrying the values for its translated
// message's {name} placeholders.
func (e *AppError) WithParams(params map[string]string) *AppError {
	c := *e
	c.params = make(map[string]string, len(params))
	for k, v := range params {
		c.params[k] = v
	}
	return &c
}

// MessageKey is the key of e's message in locales/errors.<lang>.yaml.
func (e *AppError) MessageKey() string {
	if e.variant == "" {
		return "err." + e.Code
	}
	return "err." + e.Code + "." + e.variant
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

func TooManyRequests(code, message string) *AppError {
	return New(http.StatusTooManyRequests, code, message)
}

// Internal wraps err as a 500 AppError. err.Error() may contain SQL
// fragments, driver internals, or other details that must not leak to
// unauthenticated API callers, so the client-facing Message is generic;
// middleware.Wrap logs the original err separately.
func Internal(err error) *AppError {
	return &AppError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "internal server error"}
}
