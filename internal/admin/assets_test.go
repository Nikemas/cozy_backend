package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/httpmw"
)

func TestAdminLayoutLinksContentHashedAssets(t *testing.T) {
	rr := newTestRenderer(t)
	w := httptest.NewRecorder()
	if err := rr.Render(w, "login", PageData{Screen: "login", PageTitle: "Вход"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	for _, want := range []string{
		`href="/admin/static/css/admin.css?v=` + rr.assets["css/admin.css"] + `"`,
		`src="/admin/static/img/cozy-logo.png?v=` + rr.assets["img/cozy-logo.png"] + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("admin page missing %q", want)
		}
	}
	if len(rr.assets["css/admin.css"]) != httpmw.AssetHashLen {
		t.Errorf("admin.css hash = %q, want %d hex chars", rr.assets["css/admin.css"], httpmw.AssetHashLen)
	}
}

func TestAdminStaticCachesLongOnlyForCurrentHash(t *testing.T) {
	rr := newTestRenderer(t)
	defer chdir(t, repoRoot(t))()
	h := http.StripPrefix(staticURLPrefix, httpmw.VersionedStatic(staticDir, rr.assets, staticMaxAge))

	cases := []struct{ name, url, wantCache string }{
		{"current hash", rr.assetURL("css/admin.css"), "public, max-age=31536000"},
		{"stale hash", "/admin/static/css/admin.css?v=0000000000", "public, max-age=600"},
		{"no hash", "/admin/static/css/admin.css", "public, max-age=600"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			if got := w.Header().Get("Cache-Control"); got != tc.wantCache {
				t.Errorf("Cache-Control = %q, want %q", got, tc.wantCache)
			}
		})
	}
}
