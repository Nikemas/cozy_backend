package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// newTestMux registers the real storefront routes on a fresh mux with no
// database — fine for any request that doesn't reach a repo (anonymous
// visitors on static pages, unknown URLs, redirects).
func newTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	restore := chdir(t, repoRoot(t))
	t.Cleanup(restore)
	t.Cleanup(func() { apperr.SetHTMLRenderer(nil) })

	mux := http.NewServeMux()
	cfg := &config.Config{JWTSecret: "test-secret-test-secret-test-secret", PublicBaseURL: "https://cozy.test"}
	if err := RegisterRoutes(mux, nil, cfg, nil); err != nil {
		t.Fatalf("RegisterRoutes: %v", err)
	}
	return mux
}

func TestStaticPagesServeInBothLanguages(t *testing.T) {
	mux := newTestMux(t)

	for _, name := range staticPages {
		for _, lang := range []string{i18n.LangRU, i18n.LangKY} {
			req := httptest.NewRequest(http.MethodGet, "/"+name, nil)
			req.AddCookie(&http.Cookie{Name: langCookieName, Value: lang})
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("GET /%s (%s): status = %d, want 200", name, lang, w.Code)
			}
			body := w.Body.String()
			if !strings.Contains(body, `class="static-page"`) || !strings.Contains(body, "<h1>") {
				t.Errorf("GET /%s (%s): missing page content", name, lang)
			}
			if !strings.Contains(body, `href="/privacy"`) || !strings.Contains(body, `href="/terms"`) {
				t.Errorf("GET /%s (%s): footer should link the legal pages", name, lang)
			}
		}
	}
}

func TestCatchAllServesHTML404ForSiteAndJSONForAPI(t *testing.T) {
	mux := newTestMux(t)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/definitely/not/here", nil))
	if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("unknown site URL: %d %q, want 404 text/html", w.Code, w.Header().Get("Content-Type"))
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/definitely-not-here", nil))
	if w.Code != http.StatusNotFound || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unknown API URL: %d %q, want 404 application/json", w.Code, w.Header().Get("Content-Type"))
	}
}
