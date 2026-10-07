package web

import (
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// htmxSRI is the Subresource Integrity hash of htmx.org@1.9.12's
// dist/htmx.min.js as published on unpkg. The file is vendored (no CDN at
// runtime), so the integrity guarantee moves here: the shipped bytes must be
// exactly upstream's.
const htmxSRI = "sha384-ujb1lZYygJmzgSwoxRggbCHcjc0rB2XoQrxeTUQyRjrOnlCoYta87iKBWq3EsdM2"

// selfHostedFonts are the WOFF2 files @font-face'd by both layouts
// (scripts/vendor-web-assets.py writes them into web/static and admin/static).
var selfHostedFonts = []string{
	"fonts/manrope-latin.woff2",
	"fonts/manrope-cyrillic.woff2",
	"fonts/manrope-cyrillic-ext.woff2",
	"fonts/inter-kyrgyz.woff2",
}

// externalRuntimeHosts must not appear in rendered pages: every byte the
// browser needs comes from our own origin (slow mobile internet in KG).
var externalRuntimeHosts = []string{"unpkg.com", "fonts.googleapis.com", "fonts.gstatic.com", "jsdelivr"}

func renderLangPage(t *testing.T) (*Renderer, string) {
	t.Helper()
	rr := newTestRenderer(t)
	w := httptest.NewRecorder()
	if err := rr.Render(w, "lang", PageData{Lang: i18n.LangRU, Screen: "lang"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return rr, w.Body.String()
}

func TestVendoredHtmxMatchesUpstreamSRI(t *testing.T) {
	defer chdir(t, repoRoot(t))()
	for _, p := range []string{"web/static/js/htmx.min.js", "admin/static/js/htmx.min.js"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		sum := sha512.Sum384(b)
		if got := "sha384-" + base64.StdEncoding.EncodeToString(sum[:]); got != htmxSRI {
			t.Errorf("%s integrity = %s, want %s", p, got, htmxSRI)
		}
	}
}

func TestSelfHostedFontsShipWithLicensesAndMatchAdminCopies(t *testing.T) {
	defer chdir(t, repoRoot(t))()
	for _, rel := range selfHostedFonts {
		web, err := os.ReadFile(filepath.Join("web/static", rel))
		if err != nil {
			t.Fatalf("read web copy: %v", err)
		}
		if !bytes.HasPrefix(web, []byte("wOF2")) {
			t.Errorf("%s is not a WOFF2 file", rel)
		}
		adm, err := os.ReadFile(filepath.Join("admin/static", rel))
		if err != nil {
			t.Fatalf("read admin copy: %v", err)
		}
		if !bytes.Equal(web, adm) {
			t.Errorf("%s differs between web/static and admin/static — rerun scripts/vendor-web-assets.py", rel)
		}
	}
	for _, lic := range []string{"LICENSE-manrope.txt", "LICENSE-inter.txt"} {
		for _, dir := range []string{"web/static/fonts", "admin/static/fonts"} {
			if _, err := os.Stat(filepath.Join(dir, lic)); err != nil {
				t.Errorf("OFL license must ship next to the font: %v", err)
			}
		}
	}
}

func TestLayoutUsesSelfHostedHtmxAndFonts(t *testing.T) {
	rr, body := renderLangPage(t)

	want := []string{
		`<script src="/static/js/htmx.min.js?v=` + rr.assets["js/htmx.min.js"] + `" defer></script>`,
		// The main (Cyrillic) Manrope file is preloaded so text doesn't wait
		// for the inline @font-face to be matched against rendered glyphs.
		`<link rel="preload" href="/static/fonts/manrope-cyrillic.woff2?v=` + rr.assets["fonts/manrope-cyrillic.woff2"] + `" as="font" type="font/woff2" crossorigin>`,
		`font-display: swap`,
		// Manrope has no Ң/ң: the Inter subset covers exactly those two.
		`unicode-range: U+04A2-04A3`,
	}
	for _, rel := range selfHostedFonts {
		if rr.assets[rel] == "" {
			t.Fatalf("no asset hash for %s", rel)
		}
		want = append(want, `url("/static/`+rel+`?v=`+rr.assets[rel]+`")`)
	}
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("layout missing %q", w)
		}
	}
	for _, host := range externalRuntimeHosts {
		if strings.Contains(body, host) {
			t.Errorf("layout still references %s", host)
		}
	}
}

func TestSiteFontStackFallsBackToKyrgyzSubset(t *testing.T) {
	defer chdir(t, repoRoot(t))()
	css, err := os.ReadFile("web/static/css/site.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), `--cozy-font: 'Manrope', 'Inter Kyrgyz',`) {
		t.Error("site.css --cozy-font must list 'Inter Kyrgyz' right after 'Manrope'")
	}
}

func TestSelfHostedAssetsServedWithLongCacheAndType(t *testing.T) {
	rr := newTestRenderer(t)
	defer chdir(t, repoRoot(t))()
	h := http.StripPrefix(staticURLPrefix, staticHandler(staticDir, rr.assets))

	cases := []struct{ rel, wantType string }{
		{"js/htmx.min.js", "text/javascript"},
		{"fonts/manrope-cyrillic.woff2", "font/woff2"},
		{"fonts/inter-kyrgyz.woff2", "font/woff2"},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, rr.assets.url(tc.rel), nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.wantType) {
				t.Errorf("Content-Type = %q, want %s", got, tc.wantType)
			}
			if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000" {
				t.Errorf("Cache-Control = %q", got)
			}
		})
	}
}
