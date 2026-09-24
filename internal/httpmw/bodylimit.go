package httpmw

import (
	"mime"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// LimitBody caps every request body: multipart/form-data (file uploads)
// at maxUpload, everything else (JSON, urlencoded forms, webhooks) at
// maxBody. Upload handlers still apply their own, tighter per-file limits
// (media, xlsx import) — this is the global backstop so no handler can be
// made to buffer an unbounded body, e.g. json.Decoder on a 1 GB request.
//
// A declared Content-Length over the limit is refused up front with 413;
// a chunked/lying body fails at the limit with *http.MaxBytesError from
// the handler's own read.
func LimitBody(maxBody, maxUpload int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body == nil || r.Body == http.NoBody {
				next.ServeHTTP(w, r)
				return
			}
			limit := maxBody
			if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && mt == "multipart/form-data" {
				limit = maxUpload
			}
			if r.ContentLength > limit {
				apperr.WriteError(w, r, ErrBodyTooLarge())
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// ErrBodyTooLarge is the 413 returned when a request body exceeds its
// limit — exported so JSON decoders can translate *http.MaxBytesError
// into the same response.
func ErrBodyTooLarge() error {
	return apperr.New(http.StatusRequestEntityTooLarge, "body_too_large", "слишком большой запрос")
}
