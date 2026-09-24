package apperr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsAPIPath(t *testing.T) {
	cases := map[string]bool{
		"/api/v1/products":     true,
		"/admin/api/orders":    true,
		"/":                    false,
		"/product/x":           false,
		"/admin/orders":        false,
		"/apis":                false,
		"/administrator/api/x": false,
	}
	for path, want := range cases {
		if got := IsAPIPath(path); got != want {
			t.Errorf("IsAPIPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestWriteErrorKeepsJSONForAPIPaths(t *testing.T) {
	SetHTMLRenderer(func(w http.ResponseWriter, r *http.Request, e *AppError) bool {
		t.Fatal("HTML renderer must not be called for an API path")
		return true
	})
	defer SetHTMLRenderer(nil)

	for _, path := range []string{"/api/v1/products/x", "/admin/api/orders"} {
		w := httptest.NewRecorder()
		WriteError(w, httptest.NewRequest(http.MethodGet, path, nil), NotFound("product_not_found", "товар не найден"))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", path, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("%s: Content-Type = %q, want application/json", path, ct)
		}
		var body errorBody
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Code != "product_not_found" {
			t.Fatalf("%s: body = %q (%v), want JSON with code product_not_found", path, w.Body.String(), err)
		}
	}
}

func TestWriteErrorUsesRegisteredHTMLRendererForPages(t *testing.T) {
	var got *AppError
	SetHTMLRenderer(func(w http.ResponseWriter, r *http.Request, e *AppError) bool {
		got = e
		w.WriteHeader(e.Status)
		_, _ = w.Write([]byte("<html>custom</html>"))
		return true
	})
	defer SetHTMLRenderer(nil)

	w := httptest.NewRecorder()
	WriteError(w, httptest.NewRequest(http.MethodGet, "/product/nope", nil), errors.New("boom"))
	if got == nil || got.Status != http.StatusInternalServerError {
		t.Fatalf("renderer got %+v, want a 500 AppError", got)
	}
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "custom") {
		t.Fatalf("status/body = %d %q, want the renderer's output", w.Code, w.Body.String())
	}
}

func TestWriteErrorFallsBackToMinimalHTMLWhenRendererDeclines(t *testing.T) {
	SetHTMLRenderer(func(http.ResponseWriter, *http.Request, *AppError) bool { return false })
	defer SetHTMLRenderer(nil)

	w := httptest.NewRecorder()
	WriteError(w, httptest.NewRequest(http.MethodGet, "/admin/orders/x", nil), NotFound("order_not_found", "<заказ> не найден"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(w.Body.String(), "&lt;заказ&gt; не найден") {
		t.Fatalf("body = %q, want the escaped message", w.Body.String())
	}
}
