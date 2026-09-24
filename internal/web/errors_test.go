package web

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// newTestHandlers builds a handlers value with just the renderer/bundle/
// config wired — enough for any code path that doesn't touch the
// database (an anonymous visitor's base() never queries it).
func newTestHandlers(t *testing.T) *handlers {
	t.Helper()
	rr := newTestRenderer(t)
	return &handlers{render: rr, bundle: rr.bundle, cfg: &config.Config{PublicBaseURL: "https://cozy.test"}}
}

// serveWithErrors runs fn through apperr.Wrap with h's HTML error
// renderer installed, the way RegisterRoutes wires every site route.
func serveWithErrors(t *testing.T, h *handlers, fn apperr.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	apperr.SetHTMLRenderer(h.renderHTMLError)
	t.Cleanup(func() { apperr.SetHTMLRenderer(nil) })
	w := httptest.NewRecorder()
	apperr.Wrap(fn)(w, req)
	return w
}

func TestUnknownPageRendersHTML404(t *testing.T) {
	h := newTestHandlers(t)
	w := serveWithErrors(t, h, h.notFound, httptest.NewRequest(http.MethodGet, "/no-such-page", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"Страница не найдена", `<meta name="robots" content="noindex">`, `class="header"`} {
		if !strings.Contains(body, want) {
			t.Errorf("404 page missing %q", want)
		}
	}
}

func TestUnknownAPIPathStaysJSON(t *testing.T) {
	h := newTestHandlers(t)
	w := serveWithErrors(t, h, h.notFound, httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

func TestInternalErrorRendersHTML500WithoutLeakingDetails(t *testing.T) {
	h := newTestHandlers(t)
	boom := func(http.ResponseWriter, *http.Request) error {
		return os.ErrPermission // any non-AppError → 500
	}
	w := serveWithErrors(t, h, boom, httptest.NewRequest(http.MethodGet, "/cart", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), os.ErrPermission.Error()) {
		t.Error("500 page leaks the internal error text")
	}
	if !strings.Contains(w.Body.String(), "Что-то пошло не так") {
		t.Error("500 page missing its localized title")
	}
}

func TestErrorPageUsesVisitorLanguage(t *testing.T) {
	h := newTestHandlers(t)
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	req.AddCookie(&http.Cookie{Name: langCookieName, Value: i18n.LangKY})
	w := serveWithErrors(t, h, h.notFound, req)

	if !strings.Contains(w.Body.String(), "Барак табылган жок") {
		t.Errorf("KY 404 page missing the Kyrgyz title")
	}
	if !strings.Contains(w.Body.String(), `<html lang="ky">`) {
		t.Errorf("KY 404 page should declare lang=ky")
	}
}

func TestHTMXErrorIsRetargetedToToast(t *testing.T) {
	h := newTestHandlers(t)
	fail := func(http.ResponseWriter, *http.Request) error {
		return apperr.BadRequest("variant_required", "выберите размер и цвет")
	}
	req := httptest.NewRequest(http.MethodPost, "/product/x/cart", nil)
	req.Header.Set("HX-Request", "true")
	w := serveWithErrors(t, h, fail, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := w.Header().Get("HX-Retarget"); got != "#toast-slot" {
		t.Fatalf("HX-Retarget = %q, want #toast-slot", got)
	}
	if !strings.Contains(w.Body.String(), "Выберите размер и цвет") || !strings.Contains(w.Body.String(), "toast") {
		t.Fatalf("body = %q, want a toast with the translated message", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "<html") {
		t.Fatal("HTMX error must be a fragment, not a full page")
	}
}

func TestErrorTextFallbacks(t *testing.T) {
	h := newTestHandlers(t)
	unknown := apperr.BadRequest("some_new_code", "особое сообщение")

	if got := h.errorText(i18n.LangRU, unknown); got != "особое сообщение" {
		t.Errorf("RU unknown code = %q, want the domain message", got)
	}
	if got := h.errorText(i18n.LangKY, unknown); got != h.t(i18n.LangKY, "error.generic.text") {
		t.Errorf("KY unknown code = %q, want the generic KY text (no Russian leak)", got)
	}
	if got := h.errText(i18n.LangRU, os.ErrClosed); got != h.t(i18n.LangRU, "error.internal.text") {
		t.Errorf("plain error = %q, want the generic internal text", got)
	}
}

// TestLocaleKeySetsMatch keeps ru.yaml and ky.yaml in lockstep — a key
// missing from ky.yaml would silently fall back to Russian on the KY site.
func TestLocaleKeySetsMatch(t *testing.T) {
	root := repoRoot(t)
	ru := localeKeys(t, filepath.Join(root, "locales", "ru.yaml"))
	ky := localeKeys(t, filepath.Join(root, "locales", "ky.yaml"))

	var missing []string
	for k := range ru {
		if !ky[k] {
			missing = append(missing, "ky: "+k)
		}
	}
	for k := range ky {
		if !ru[k] {
			missing = append(missing, "ru: "+k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("locale key sets differ:\n%s", strings.Join(missing, "\n"))
	}
}

func localeKeys(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	keys := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, ":"); i > 0 {
			key := strings.TrimSpace(line[:i])
			if keys[key] {
				t.Errorf("%s: duplicate key %q", filepath.Base(path), key)
			}
			keys[key] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return keys
}
