package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// pointsFailingImporter is a fakeImporter whose point list cannot load.
type pointsFailingImporter struct{ fakeImporter }

func (*pointsFailingImporter) ActivePoints(context.Context) ([]catalog.ImportPoint, error) {
	return nil, errors.New("points down")
}

func TestImportCheckRejectsBadUploads(t *testing.T) {
	notMultipart := func(t *testing.T) *http.Request {
		r := httptest.NewRequest(http.MethodPost, importCheckPath, strings.NewReader("plain"))
		r.Header.Set("Content-Type", "text/plain")
		r.Header.Set("HX-Request", "true")
		return r.WithContext(staff.NewContextWithStaff(r.Context(), manager()))
	}
	tests := []struct {
		name    string
		request func(t *testing.T) *http.Request
		wantKey string
	}{
		{"not a multipart form", notMultipart, "admin.import.err_upload"},
		{"unsupported file type", func(t *testing.T) *http.Request {
			return importRequestNamed(t, importCheckPath, "goods.pdf", "%PDF", "", "")
		}, "admin.import.err_format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imp := &fakeImporter{result: cleanImportResult()}
			h := newImportHandlers(t, imp)
			w := httptest.NewRecorder()

			h.productImportCheck(w, tt.request(t))

			if len(imp.calls) != 0 {
				t.Errorf("importer called %d times for a rejected upload", len(imp.calls))
			}
			if body := w.Body.String(); !strings.Contains(body, ruTr.T(tt.wantKey)) {
				t.Errorf("want %q in:\n%s", ruTr.T(tt.wantKey), body)
			}
		})
	}
}

func TestImportUnexpectedErrorShowsGenericMessage(t *testing.T) {
	imp := &fakeImporter{result: cleanImportResult()}
	h := newImportHandlers(t, imp)
	token := checkToken(t, h, "goods.csv", "A")
	imp.err = errors.New("connection reset")

	for _, path := range []string{importCheckPath, "/admin/products/import/apply"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := importRequestNamed(t, path, "goods.csv", "A", "pt1", token)

			if path == importCheckPath {
				h.productImportCheck(w, r)
			} else {
				h.productImportApply(w, r)
			}

			body := w.Body.String()
			if !strings.Contains(body, ruTr.T("admin.err.generic")) {
				t.Errorf("want the generic error in:\n%s", body)
			}
			if strings.Contains(body, "connection reset") {
				t.Errorf("internal error text leaked into the page")
			}
		})
	}
}

func TestImportPageWhenPointsCannotLoad(t *testing.T) {
	h := &handlers{render: newTestRenderer(t), importer: &pointsFailingImporter{}, importTokenKey: []byte("k")}
	w := httptest.NewRecorder()

	h.productImportPage(w, requestAs(http.MethodGet, "/admin/products/import", manager(), nil))

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<html") {
		t.Fatalf("status %d, want the page rendered with default point only:\n%s", w.Code, w.Body.String())
	}
	points, failed := h.importPoints(context.Background())
	if points != nil || !failed {
		t.Errorf("importPoints = %v, %v; want nil, true", points, failed)
	}
}

func TestImportPointsWithoutImporter(t *testing.T) {
	h := &handlers{}
	if points, failed := h.importPoints(context.Background()); points != nil || !failed {
		t.Errorf("importPoints = %v, %v; want nil, true", points, failed)
	}
}

func TestImportBadRowLineAndStatusLabel(t *testing.T) {
	tests := []struct {
		name string
		row  catalog.ImportRowResult
		want string
	}{
		{"model and message", catalog.ImportRowResult{Row: 3, Model: "CZ-1", Message: "Не указана цена"},
			ruTr.T("admin.import.row") + " 3 (CZ-1) — Не указана цена"},
		{"no model, falls back to status", catalog.ImportRowResult{Row: 4, Status: catalog.RowStatusSkipped},
			ruTr.T("admin.import.row") + " 4 — " + ruTr.T("admin.import.st_will_skip")},
		{"unknown status shown as is", catalog.ImportRowResult{Row: 5, Status: "weird"},
			ruTr.T("admin.import.row") + " 5 — weird"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := importBadRowLine(ruTr, tt.row); got != tt.want {
				t.Errorf("importBadRowLine = %q, want %q", got, tt.want)
			}
		})
	}
	if got := importStatusLabel(ruTr, catalog.RowStatusCreated, false); got != ruTr.T("admin.import.st_created") {
		t.Errorf("applied created label = %q", got)
	}
}
