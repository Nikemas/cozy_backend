package httpmw

import (
	"fmt"
	"net/http"
	"path"
	"strconv"
	"time"
)

// PublicCache marks successful (200) GET/HEAD responses as cacheable by
// browsers, the mobile app's HTTP client and any shared cache for maxAge,
// plus a stale-while-revalidate window of the same length. Only for
// endpoints whose response is identical for every caller — no auth, no
// cookies, no per-user fields (the public catalog, points of sale). Error
// responses and other methods are left untouched so a transient 500 or
// 404 is never cached.
func PublicCache(maxAge time.Duration) func(http.Handler) http.Handler {
	secs := int(maxAge / time.Second)
	value := fmt.Sprintf("public, max-age=%d, stale-while-revalidate=%d", secs, secs)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(&cacheHeaderWriter{ResponseWriter: w, value: value}, r)
		})
	}
}

// cacheHeaderWriter sets Cache-Control just before the status line goes
// out, and only if that status is 200 and the handler didn't set its own.
type cacheHeaderWriter struct {
	http.ResponseWriter
	value       string
	wroteHeader bool
}

func (c *cacheHeaderWriter) WriteHeader(code int) {
	if !c.wroteHeader {
		c.wroteHeader = true
		if code == http.StatusOK && c.Header().Get("Cache-Control") == "" {
			c.Header().Set("Cache-Control", c.value)
		}
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *cacheHeaderWriter) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}

func (c *cacheHeaderWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// Static serves files from dir (like http.FileServer(http.Dir(dir)), to be
// mounted behind http.StripPrefix) with caching headers: a weak ETag
// derived from the file's size and mtime — which http.FileServer then
// honors for If-None-Match, answering 304 without a body — and
// Cache-Control: public, max-age=maxAge. Asset URLs are not
// content-hashed yet (templates link plain /static/css/site.css), so
// maxAge must stay short enough that a deploy's CSS change shows up
// promptly; the ETag makes every revalidation after that a cheap 304.
func Static(dir string, maxAge time.Duration) http.Handler {
	root := http.Dir(dir)
	files := http.FileServer(root)
	cacheControl := "public, max-age=" + strconv.Itoa(int(maxAge/time.Second))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f, err := root.Open(path.Clean("/" + r.URL.Path)); err == nil {
			if fi, err := f.Stat(); err == nil && !fi.IsDir() {
				w.Header().Set("ETag", fmt.Sprintf(`W/"%x-%x"`, fi.Size(), fi.ModTime().UnixNano()))
				w.Header().Set("Cache-Control", cacheControl)
			}
			_ = f.Close()
		}
		files.ServeHTTP(w, r)
	})
}
