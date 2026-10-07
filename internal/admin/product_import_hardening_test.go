package admin

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// importClock is a manually advanced clock for the token's issued-at.
type importClock struct{ t time.Time }

func (c *importClock) now() time.Time { return c.t }

// fakeImportLimiter allows the first n calls, then refuses; it records
// the keys it was asked about.
type fakeImportLimiter struct {
	n    int
	keys []string
}

func (f *fakeImportLimiter) Allow(key string) (bool, time.Duration) {
	f.keys = append(f.keys, key)
	if len(f.keys) > f.n {
		return false, 42 * time.Second
	}
	return true, 0
}

// importRequestNamed is importRequest with the uploaded file's name.
func importRequestNamed(t *testing.T, target, filename, file, pointID, token string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte(file))
	_ = mw.WriteField("point_id", pointID)
	_ = mw.WriteField("checked_token", token)
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, target, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("HX-Request", "true")
	return r.WithContext(staff.NewContextWithStaff(r.Context(), manager()))
}

// checkToken runs a clean check of file and returns the issued token.
func checkToken(t *testing.T, h *handlers, filename, file string) string {
	t.Helper()
	w := httptest.NewRecorder()
	h.productImportCheck(w, importRequestNamed(t, importCheckPath, filename, file, "pt1", ""))
	m := tokenRE.FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatalf("clean check issued no token:\n%s", w.Body.String())
	}
	return m[1]
}

func TestImportTokenExpiresAfterTTL(t *testing.T) {
	cases := []struct {
		name        string
		after       time.Duration
		wantApplied bool
	}{
		{"fresh", time.Minute, true},
		{"just inside", importTokenTTL, true},
		{"expired", importTokenTTL + time.Second, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Arrange
			imp := &fakeImporter{result: cleanImportResult()}
			h := newImportHandlers(t, imp)
			clock := &importClock{t: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
			h.importNow = clock.now
			token := checkToken(t, h, "goods.csv", "A")
			clock.t = clock.t.Add(c.after)
			imp.calls = nil
			w := httptest.NewRecorder()

			// Act
			h.productImportApply(w, importRequestNamed(t, importApplyPath, "goods.csv", "A", "pt1", token))

			// Assert
			applied := len(imp.calls) == 1 && !imp.calls[0].DryRun
			if applied != c.wantApplied {
				t.Fatalf("applied = %v, want %v", applied, c.wantApplied)
			}
			if !c.wantApplied && !strings.Contains(w.Body.String(), ruTr.T("admin.import.err_recheck")) {
				t.Errorf("expired token must ask to check again:\n%s", w.Body.String())
			}
		})
	}
}

func TestImportTokenRejectsTamperedOrMalformed(t *testing.T) {
	// Arrange
	imp := &fakeImporter{result: cleanImportResult()}
	h := newImportHandlers(t, imp)
	clock := &importClock{t: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	h.importNow = clock.now
	token := checkToken(t, h, "goods.csv", "A")
	ts, mac, ok := strings.Cut(token, ".")
	if !ok || ts == "" || mac == "" {
		t.Fatalf("token %q is not ts.mac", token)
	}
	cases := map[string]string{
		"later timestamp, same mac": "ffffffff." + mac,
		"no timestamp":              mac,
		"bad timestamp":             "zz." + mac,
		"empty mac":                 ts + ".",
		"other format":              "", // filled below: right token, .xlsx upload
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			filename := "goods.csv"
			if name == "other format" {
				tok, filename = token, "goods.xlsx"
			}
			imp.calls = nil
			w := httptest.NewRecorder()

			// Act
			h.productImportApply(w, importRequestNamed(t, importApplyPath, filename, "A", "pt1", tok))

			// Assert
			if len(imp.calls) != 0 {
				t.Fatalf("import ran with token %q (%s)", tok, filename)
			}
			if !strings.Contains(w.Body.String(), ruTr.T("admin.import.err_recheck")) {
				t.Errorf("want err_recheck:\n%s", w.Body.String())
			}
		})
	}
}

var runSlotRE = regexp.MustCompile(`<span id="import-run-slot"[^>]*>`)

func TestImportPageHidesRunWithoutJS(t *testing.T) {
	// Arrange: no JS — the check form posts normally.
	imp := &fakeImporter{result: cleanImportResult()}
	h := newImportHandlers(t, imp)
	w := httptest.NewRecorder()

	// Act
	h.productImportCheck(w, importRequest(t, importCheckPath, "A", "", "", false))

	// Assert: the report is there, but no apply path that can't work.
	body := w.Body.String()
	if !strings.Contains(body, ruTr.T("admin.import.dry_ok")) {
		t.Errorf("no-JS check must still show the report:\n%s", body)
	}
	if slot := runSlotRE.FindString(body); !strings.Contains(slot, "hidden") {
		t.Errorf("run slot must be hidden until JS reveals it: %q", slot)
	}
	if tokenRE.MatchString(body) {
		t.Error("a full-page check must not issue a token: without JS the file can't be re-sent")
	}
	if !strings.Contains(body, "<noscript>") || !strings.Contains(body, ruTr.T("admin.import.needs_js")) {
		t.Error("page must explain in a <noscript> that importing needs JavaScript")
	}
}

func TestImportFragmentRunSlotIsVisible(t *testing.T) {
	imp := &fakeImporter{result: cleanImportResult()}
	h := newImportHandlers(t, imp)
	w := httptest.NewRecorder()

	h.productImportCheck(w, importRequest(t, importCheckPath, "A", "", "", true))

	if slot := runSlotRE.FindString(w.Body.String()); slot == "" || strings.Contains(slot, "hidden") {
		t.Errorf("htmx fragment's run slot must be visible: %q", slot)
	}
}

func TestImportRateLimitedPerStaff(t *testing.T) {
	type handler func(*handlers, http.ResponseWriter, *http.Request)
	endpoints := map[string]struct {
		path string
		fn   handler
	}{
		"check": {importCheckPath, (*handlers).productImportCheck},
		"apply": {importApplyPath, (*handlers).productImportApply},
	}
	for name, ep := range endpoints {
		for _, htmx := range []bool{true, false} {
			t.Run(name+map[bool]string{true: "/htmx", false: "/page"}[htmx], func(t *testing.T) {
				// Arrange: the limiter has no tokens left.
				imp := &fakeImporter{result: cleanImportResult()}
				h := newImportHandlers(t, imp)
				lim := &fakeImportLimiter{n: 0}
				h.importLimiter = lim
				w := httptest.NewRecorder()

				// Act
				ep.fn(h, w, importRequest(t, ep.path, "A", "pt1", "tok", htmx))

				// Assert
				if w.Code != http.StatusTooManyRequests {
					t.Errorf("status = %d, want 429", w.Code)
				}
				if got := w.Header().Get("Retry-After"); got != "42" {
					t.Errorf("Retry-After = %q, want 42", got)
				}
				if len(imp.calls) != 0 {
					t.Errorf("importer called while rate limited: %+v", imp.calls)
				}
				if len(lim.keys) != 1 || lim.keys[0] != manager().ID {
					t.Errorf("limiter keys = %v, want the staff ID %q", lim.keys, manager().ID)
				}
				body := w.Body.String()
				if !strings.Contains(body, ruTr.T("admin.import.err_rate_limited")) {
					t.Errorf("want the localized rate-limit message:\n%s", body)
				}
				if htmx && strings.Contains(body, "<html") {
					t.Error("htmx request must get the fragment, not the page")
				}
			})
		}
	}
}

func TestImportRateLimitAllowsUnderTheLimit(t *testing.T) {
	imp := &fakeImporter{result: cleanImportResult()}
	h := newImportHandlers(t, imp)
	h.importLimiter = &fakeImportLimiter{n: 1}
	w := httptest.NewRecorder()

	h.productImportCheck(w, importRequest(t, importCheckPath, "A", "pt1", "", true))

	if w.Code != http.StatusOK || len(imp.calls) != 1 {
		t.Errorf("status = %d, calls = %d; want 200 and one dry run", w.Code, len(imp.calls))
	}
}
