package httpmw

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/reqid"
)

// captureLogs points the default slog logger (wrapped in reqid.LogHandler,
// as cmd/server does) at a buffer for the duration of the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(reqid.NewLogHandler(slog.NewTextHandler(&buf, nil))))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestRequestIDGeneratesAndPropagates(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = reqid.FromContext(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	got := rec.Header().Get(reqid.Header)
	if !reqid.Valid(got) || got != seen {
		t.Fatalf("response id %q, context id %q: want the same valid generated id", got, seen)
	}
}

func TestRequestIDAdoptsValidClientID(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(reqid.Header, "client-id-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get(reqid.Header); got != "client-id-1" {
		t.Fatalf("X-Request-ID = %q, want client-id-1", got)
	}
}

func TestRequestIDReplacesMalformedClientID(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(reqid.Header, "evil\nid")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get(reqid.Header); got == "evil\nid" || !reqid.Valid(got) {
		t.Fatalf("X-Request-ID = %q, want a freshly generated id", got)
	}
}

func TestAppErrBodyCarriesRequestID(t *testing.T) {
	h := RequestID(apperr.Wrap(func(http.ResponseWriter, *http.Request) error {
		return apperr.NotFound("nope", "not here")
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(reqid.Header, "rid-7")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if body["request_id"] != "rid-7" || body["code"] != "nope" {
		t.Fatalf("body = %v, want code=nope request_id=rid-7", body)
	}
}

func TestAccessLogRecordsStatusBytesAndRequestID(t *testing.T) {
	logs := captureLogs(t)
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello"))
	}), RequestID, AccessLog)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/x", nil)
	req.Header.Set(reqid.Header, "rid-log")
	h.ServeHTTP(httptest.NewRecorder(), req)

	line := logs.String()
	for _, want := range []string{"method=POST", "path=/api/v1/x", "status=418", "bytes=5", "request_id=rid-log", "duration="} {
		if !strings.Contains(line, want) {
			t.Errorf("access log %q missing %q", line, want)
		}
	}
}

func TestAccessLogDefaultsTo200AndSkipsHealth(t *testing.T) {
	logs := captureLogs(t)
	h := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if logs.Len() != 0 {
		t.Fatalf("health probe was logged: %q", logs.String())
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(logs.String(), "status=200") {
		t.Fatalf("access log %q, want implicit status=200", logs.String())
	}
}

func TestRecoverReturnsAppErr500(t *testing.T) {
	logs := captureLogs(t)
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), RequestID, AccessLog, Recover)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(reqid.Header, "rid-panic")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if body["code"] != "internal_error" || body["request_id"] != "rid-panic" {
		t.Fatalf("body = %v, want internal_error with request_id", body)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("panic value leaked to client: %s", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "panic recovered") || !strings.Contains(logs.String(), "status=500") {
		t.Fatalf("logs = %q, want the panic and a status=500 access line", logs.String())
	}
}

func TestRecoverRepanicsErrAbortHandler(t *testing.T) {
	h := Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler re-panicked", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestPublicCacheOnlyOn200Get(t *testing.T) {
	status := http.StatusOK
	h := PublicCache(30 * time.Second)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/products", nil))
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=30, stale-while-revalidate=30" {
		t.Fatalf("Cache-Control = %q on 200 GET", got)
	}

	status = http.StatusNotFound
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/products/x", nil))
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("Cache-Control = %q on 404, want none", got)
	}

	status = http.StatusOK
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/products", nil))
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("Cache-Control = %q on POST, want none", got)
	}
}

func TestPublicCacheImplicit200ViaWrite(t *testing.T) {
	h := PublicCache(time.Minute)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{}"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.HasPrefix(rec.Header().Get("Cache-Control"), "public, max-age=60") {
		t.Fatalf("Cache-Control = %q, want max-age=60", rec.Header().Get("Cache-Control"))
	}
}

func TestStaticSetsETagAndAnswers304(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "css", "site.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := http.StripPrefix("/static/", Static(dir, 10*time.Minute))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/css/site.css", nil))
	etag := rec.Header().Get("ETag")
	if rec.Code != http.StatusOK || etag == "" || rec.Header().Get("Cache-Control") != "public, max-age=600" {
		t.Fatalf("status=%d etag=%q cache=%q, want 200 with ETag and max-age=600", rec.Code, etag, rec.Header().Get("Cache-Control"))
	}

	req := httptest.NewRequest(http.MethodGet, "/static/css/site.css", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304 for matching If-None-Match", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/missing.css", nil))
	if rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "" {
		t.Fatalf("missing file: status=%d cache=%q, want uncached 404", rec.Code, rec.Header().Get("Cache-Control"))
	}
}
