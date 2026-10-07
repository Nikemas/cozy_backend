//go:build integration

package integration

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/importguard"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// importUploadBody is a small multipart import upload: a dry run of a
// CSV with one row (whose unknown category only yields a row error).
func importUploadBody(t *testing.T) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("dry_run", "1"); err != nil {
		t.Fatal(err)
	}
	part, err := mw.CreateFormFile("file", "guard.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("name_ru,category,price\nКеды,no-such-category,1990\n")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

// htmlImportCheck posts to the import page's htmx check.
func (a *testApp) htmlImportCheck(t *testing.T, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	body, ctype := importUploadBody(t)
	return a.do(t, req{method: http.MethodPost, path: "/admin/products/import/check", body: body, ctype: ctype, cookies: []*http.Cookie{c}, htmx: true})
}

// jsonImport posts to the JSON import endpoint as an API client.
func (a *testApp) jsonImport(t *testing.T, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	body, ctype := importUploadBody(t)
	hr := httptest.NewRequest(http.MethodPost, "/admin/products/import", body)
	hr.Header.Set("Content-Type", ctype)
	hr.Header.Set("Accept", "application/json")
	hr.AddCookie(c)
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, hr)
	return w
}

func wantImportRefusal(t *testing.T, w *httptest.ResponseRecorder, code string) {
	t.Helper()
	wantStatus(t, w, http.StatusTooManyRequests)
	if w.Header().Get("Retry-After") == "" {
		t.Error("a 429 must carry Retry-After")
	}
	if code == "" {
		return
	}
	var env struct {
		Code string `json:"code"`
	}
	decodeJSON(t, w, &env)
	if env.Code != code {
		t.Errorf("code = %q, want %q", env.Code, code)
	}
}

// One per-staff bucket across the HTML page and the JSON endpoint, in
// either order.
func TestImportRateLimitSharedAcrossHTMLAndJSON(t *testing.T) {
	t.Parallel()
	a := app(t)
	type send func(*testing.T, *http.Cookie) *httptest.ResponseRecorder
	cases := map[string]struct {
		spend, over send
		overCode    string
	}{
		"html then json": {a.htmlImportCheck, a.jsonImport, "import_rate_limited"},
		"json then html": {a.jsonImport, a.htmlImportCheck, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cookie, _ := a.newStaffSession(t, staff.RoleManager, nil)
			for i := range importguard.RateBurst {
				if w := c.spend(t, cookie); w.Code == http.StatusTooManyRequests {
					t.Fatalf("request %d within the burst refused: %s", i, w.Body.String())
				}
			}
			wantImportRefusal(t, c.over(t, cookie), c.overCode)

			other, _ := a.newStaffSession(t, staff.RoleManager, nil)
			wantStatus(t, a.jsonImport(t, other), http.StatusOK)
		})
	}
}

// Both endpoints take their slots from the one shared gate. Not
// parallel: it holds every slot of the app-wide gate while it runs.
func TestImportGateSharedAcrossHTMLAndJSON(t *testing.T) {
	a := app(t)
	cookie, _ := a.newStaffSession(t, staff.RoleManager, nil)
	gate := a.importGuard.Gate
	releases := make([]func(), 0, gate.Cap())
	for range gate.Cap() {
		rel, ok := gate.Acquire(context.Background())
		if !ok {
			t.Fatal("setup: could not take a slot")
		}
		releases = append(releases, rel)
	}

	wantImportRefusal(t, a.jsonImport(t, cookie), "import_busy")
	wantImportRefusal(t, a.htmlImportCheck(t, cookie), "")

	for _, rel := range releases {
		rel()
	}
	wantStatus(t, a.jsonImport(t, cookie), http.StatusOK)
	wantStatus(t, a.htmlImportCheck(t, cookie), http.StatusOK)
}
