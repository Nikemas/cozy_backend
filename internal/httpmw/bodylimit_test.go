package httpmw

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLimitBody(t *testing.T) {
	var readErr error
	var readN int
	h := LimitBody(10, 100)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		readN, readErr = len(b), err
	}))

	cases := []struct {
		name, ctype string
		size        int
		chunked     bool
		wantStatus  int
		wantReadErr bool
	}{
		{"small json", "application/json", 10, false, 200, false},
		{"json over limit by Content-Length", "application/json", 11, false, 413, false},
		{"chunked json over limit", "application/json", 50, true, 200, true},
		{"multipart uses upload limit", "multipart/form-data; boundary=x", 50, false, 200, false},
		{"multipart over upload limit", "multipart/form-data; boundary=x", 101, false, 413, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			readErr, readN = nil, 0
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", c.size)))
			req.Header.Set("Content-Type", c.ctype)
			if c.chunked {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, c.wantStatus)
			}
			var mbe *http.MaxBytesError
			if c.wantReadErr != errors.As(readErr, &mbe) {
				t.Errorf("read err = %v (n=%d), want MaxBytesError=%v", readErr, readN, c.wantReadErr)
			}
		})
	}
}
