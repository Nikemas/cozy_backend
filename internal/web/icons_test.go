package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// The site self-hosts a Tabler Icons subset (web/static/css/icons.css +
// web/static/fonts/tabler-icons.woff2, built by
// scripts/tabler-icons-subset.py). An icon class missing from the subset
// renders as an empty box, so these tests fail instead.

const iconsCSSPath = "web/static/css/icons.css"

// Same patterns as scripts/tabler-icons-subset.py: any ti-* token in a
// template (also catches `ti {{if ...}}ti-trash{{else}}ti-minus{{end}}`),
// and only `ti ti-*` class strings in Go code, whose prose contains
// unrelated "...ti-..." words.
var (
	templateIconRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(ti-[a-z0-9]+(?:-[a-z0-9]+)*)`)
	goIconRe       = regexp.MustCompile(`\bti (ti-[a-z0-9]+(?:-[a-z0-9]+)*)`)
	cssIconRuleRe  = regexp.MustCompile(`\.(ti-[a-z0-9-]+):before\{content:"\\[0-9a-f]+"\}`)
	cssFontURLRe   = regexp.MustCompile(`url\("\.\./fonts/tabler-icons\.woff2\?v=([0-9a-f]+)"\)`)
)

// usedIcons maps each ti-* class found in the repo to one file using it.
func usedIcons(t *testing.T) map[string]string {
	t.Helper()
	used := map[string]string{}
	scan := func(root, ext string, re *regexp.Regexp) {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ext) || strings.HasSuffix(p, "_test.go") {
				return err
			}
			b, err := os.ReadFile(p) //nolint:gosec // walking our own source tree
			if err != nil {
				return err
			}
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				used[m[1]] = p
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scanning %s: %v", root, err)
		}
	}
	scan("web/templates", ".gohtml", templateIconRe)
	scan("admin/templates", ".gohtml", templateIconRe)
	scan("internal", ".go", goIconRe)
	scan("cmd", ".go", goIconRe)
	return used
}

func readIconsCSS(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(iconsCSSPath)
	if err != nil {
		t.Fatalf("read %s: %v", iconsCSSPath, err)
	}
	return string(b)
}

func TestEveryUsedIconIsInTheSubset(t *testing.T) {
	defer chdir(t, repoRoot(t))()

	css := readIconsCSS(t)
	have := map[string]bool{}
	for _, m := range cssIconRuleRe.FindAllStringSubmatch(css, -1) {
		have[m[1]] = true
	}
	used := usedIcons(t)
	if len(used) == 0 {
		t.Fatal("found no ti-* icons in templates; scanner is broken")
	}

	var missing []string
	for name, file := range used {
		if !have[name] {
			missing = append(missing, name+" ("+file+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("icons not in %s — run `python3 scripts/tabler-icons-subset.py`:\n  %s",
			iconsCSSPath, strings.Join(missing, "\n  "))
	}
}

func TestIconsCSSFontURLMatchesFontHash(t *testing.T) {
	defer chdir(t, repoRoot(t))()

	m := cssFontURLRe.FindStringSubmatch(readIconsCSS(t))
	if m == nil {
		t.Fatalf("%s: no url(\"../fonts/tabler-icons.woff2?v=...\")", iconsCSSPath)
	}
	font, err := os.ReadFile("web/static/fonts/tabler-icons.woff2")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(font)
	// The layout preloads asset("fonts/tabler-icons.woff2"); that URL must
	// equal the one in icons.css or the browser fetches the font twice and
	// the CSS's copy misses the year-long cache.
	if want := hex.EncodeToString(sum[:])[:assetHashLen]; m[1] != want {
		t.Errorf("icons.css font ?v=%s, want %s — rerun scripts/tabler-icons-subset.py", m[1], want)
	}
	if _, err := os.Stat("web/static/fonts/LICENSE-tabler-icons.txt"); err != nil {
		t.Errorf("Tabler MIT license must ship next to the font: %v", err)
	}
}

func TestLayoutUsesSelfHostedIcons(t *testing.T) {
	rr := newTestRenderer(t)
	w := httptest.NewRecorder()
	if err := rr.Render(w, "lang", PageData{Lang: i18n.LangRU, Screen: "lang"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	for _, want := range []string{
		`<link rel="stylesheet" href="/static/css/icons.css?v=` + rr.assets["css/icons.css"] + `">`,
		`<link rel="preload" href="/static/fonts/tabler-icons.woff2?v=` + rr.assets["fonts/tabler-icons.woff2"] + `" as="font" type="font/woff2" crossorigin>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("layout missing %q", want)
		}
	}
	if strings.Contains(body, "jsdelivr") {
		t.Error("layout still references cdn.jsdelivr.net")
	}
}
