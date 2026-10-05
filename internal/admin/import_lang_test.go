package admin

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The import page's fetch() calls must carry the page language, so the
// JSON endpoint answers row messages and file errors in it (and JSON
// errors rather than an HTML page).
func TestImportPageSendsItsLanguage(t *testing.T) {
	rr := newTestRenderer(t)
	page := kyScreenFixtures()["product_import"]
	w := httptest.NewRecorder()
	if err := rr.Render(w, "product_import", page); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	for _, want := range []string{
		`var apiHeaders = { 'Accept': 'application/json', 'Accept-Language': "ky" };`,
		`fetch(importURL, { method: 'POST', body: form, credentials: 'same-origin', headers: apiHeaders })`,
		`fetch(importURL + '/points', { credentials: 'same-origin', headers: apiHeaders })`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("import page lacks %q", want)
		}
	}
}
