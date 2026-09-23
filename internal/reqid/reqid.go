// Package reqid carries a per-request correlation ID through
// context.Context and into slog records. It is deliberately tiny and
// dependency-free so that low-level packages (apperr) can read the ID
// without importing the HTTP middleware that sets it (internal/httpmw).
package reqid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
)

// Header is the HTTP header the ID is accepted from and echoed back in.
const Header = "X-Request-ID"

type ctxKey struct{}

// NewContext returns a copy of ctx carrying id.
func NewContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the request ID stored in ctx, or "" if none.
func FromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// New generates a fresh random 16-byte (32 hex char) ID.
func New() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails on supported platforms
	return hex.EncodeToString(b[:])
}

// Valid reports whether an ID received from a client is safe to adopt:
// non-empty, at most 128 chars, and only [A-Za-z0-9._:-]. Anything else
// (including log-injection attempts with newlines) is replaced by New.
func Valid(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}

// LogHandler wraps an slog.Handler so every record logged with a context
// (slog.InfoContext, logger.ErrorContext, ...) that carries a request ID
// gets a "request_id" attribute automatically.
type LogHandler struct {
	slog.Handler
}

// NewLogHandler wraps h. See LogHandler.
func NewLogHandler(h slog.Handler) *LogHandler {
	return &LogHandler{Handler: h}
}

// Handle implements slog.Handler.
func (h *LogHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := FromContext(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs implements slog.Handler, keeping the wrapper in place.
func (h *LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler, keeping the wrapper in place.
func (h *LogHandler) WithGroup(name string) slog.Handler {
	return &LogHandler{Handler: h.Handler.WithGroup(name)}
}
