package admin

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAdminLayoutUsesSelfHostedHtmxAndFonts(t *testing.T) {
	rr := newTestRenderer(t)
	w := httptest.NewRecorder()
	if err := rr.Render(w, "login", PageData{Screen: "login", PageTitle: "Вход"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()

	want := []string{
		`<script src="` + rr.assetURL("js/htmx.min.js") + `" defer></script>`,
		`<link rel="preload" href="` + rr.assetURL("fonts/manrope-cyrillic.woff2") + `" as="font" type="font/woff2" crossorigin>`,
		`font-display: swap`,
		`unicode-range: U+04A2-04A3`,
	}
	for _, rel := range []string{
		"fonts/manrope-latin.woff2",
		"fonts/manrope-cyrillic.woff2",
		"fonts/manrope-cyrillic-ext.woff2",
		"fonts/inter-kyrgyz.woff2",
	} {
		if rr.assets[rel] == "" {
			t.Fatalf("no asset hash for %s", rel)
		}
		want = append(want, `url("`+rr.assetURL(rel)+`")`)
	}
	for _, s := range want {
		if !strings.Contains(body, s) {
			t.Errorf("admin layout missing %q", s)
		}
	}
	for _, host := range []string{"unpkg.com", "fonts.googleapis.com", "fonts.gstatic.com"} {
		if strings.Contains(body, host) {
			t.Errorf("admin layout still references %s", host)
		}
	}
}

func TestAdminFontStackFallsBackToKyrgyzSubset(t *testing.T) {
	defer chdir(t, repoRoot(t))()
	css, err := os.ReadFile("admin/static/css/admin.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), `font-family: 'Manrope', 'Inter Kyrgyz',`) {
		t.Error("admin.css body font-family must list 'Inter Kyrgyz' right after 'Manrope'")
	}
}
