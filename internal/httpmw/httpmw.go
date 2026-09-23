// Package httpmw holds cross-cutting net/http middleware for the whole
// server: request IDs, access logging, panic recovery and HTTP caching
// headers. Wired once in cmd/server/main.go (and, for the caching helpers,
// on the individual public routes that are safe to cache).
package httpmw

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/reqid"
)

// Chain applies middleware so that the first one listed is the outermost:
// Chain(h, a, b) == a(b(h)).
func Chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// RequestID adopts the client's X-Request-ID if it is well-formed (see
// reqid.Valid) or generates a new one, stores it in the request context
// (reqid.FromContext; slog *Context calls pick it up via reqid.LogHandler,
// apperr error bodies echo it) and sets it on the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(reqid.Header)
		if !reqid.Valid(id) {
			id = reqid.New()
		}
		w.Header().Set(reqid.Header, id)
		next.ServeHTTP(w, r.WithContext(reqid.NewContext(r.Context(), id)))
	})
}

// statusRecorder captures the status code and body size written through
// it. Unwrap lets http.ResponseController reach the underlying writer's
// Flush/Hijack/deadline methods; Flush is forwarded explicitly too for
// code that type-asserts http.Flusher directly.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// quietPaths are polled by the deploy script / monitoring every few
// seconds; logging each hit would drown out real traffic.
var quietPaths = map[string]bool{"/healthz": true, "/readyz": true}

// AccessLog logs one line per request (method, path, status, duration,
// response bytes, client IP) — Info below 500, Error for 5xx — with the
// request's context so request_id is attached. Health probes are skipped.
// Must sit outside Recover so a recovered panic is logged as its 500.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if quietPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if rec.status >= http.StatusInternalServerError {
			level = slog.LevelError
		}
		slog.LogAttrs(r.Context(), level, "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Duration("duration", time.Since(start)),
			slog.Int64("bytes", rec.bytes),
			slog.String("remote", clientIP(r)),
		)
	})
}

// clientIP prefers the first X-Forwarded-For hop (Caddy sets it) over
// RemoteAddr, which is always Caddy's own container address in prod. Used
// only for logging, never for any security decision.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	return r.RemoteAddr
}

// Recover turns a panic anywhere below it into a logged stack-carrying
// error and a standard apperr JSON 500 (with request_id), instead of
// net/http's default of silently dropping the connection. The
// http.ErrAbortHandler sentinel is re-panicked, as net/http expects.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			slog.ErrorContext(r.Context(), "panic recovered", "path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			apperr.WriteError(w, r, fmt.Errorf("panic: %v", v))
		}()
		next.ServeHTTP(w, r)
	})
}
