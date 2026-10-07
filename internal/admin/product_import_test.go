package admin

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// fakeImporter records ImportProducts calls and returns a canned result.
type fakeImporter struct {
	calls  []catalog.ImportOptions
	files  []string
	result *catalog.ImportResult
	err    error
	points []catalog.ImportPoint
}

func (f *fakeImporter) Import(_ context.Context, r io.Reader, _ catalog.ImportFormat, opts catalog.ImportOptions) (*catalog.ImportResult, error) {
	b, _ := io.ReadAll(r)
	f.calls = append(f.calls, opts)
	f.files = append(f.files, string(b))
	if f.err != nil {
		return nil, f.err
	}
	res := *f.result
	res.DryRun = opts.DryRun
	return &res, nil
}

func (f *fakeImporter) ActivePoints(context.Context) ([]catalog.ImportPoint, error) {
	return f.points, nil
}

func cleanImportResult() *catalog.ImportResult {
	return &catalog.ImportResult{
		Summary: catalog.ImportSummary{Rows: 1, Created: 1, ProductsCreated: 1, VariantsCreated: 1},
		Rows:    []catalog.ImportRowResult{{Row: 2, Status: catalog.RowStatusCreated, Model: "CZ-1", Size: "40", Color: "Белый"}},
	}
}

func erroredImportResult() *catalog.ImportResult {
	return &catalog.ImportResult{
		Summary: catalog.ImportSummary{Rows: 2, Errors: 1, Skipped: 1},
		Rows: []catalog.ImportRowResult{
			{Row: 2, Status: catalog.RowStatusSkipped, Model: "CZ-1", Message: "Модель не импортирована"},
			{Row: 3, Status: catalog.RowStatusError, Model: "CZ-1", Message: "Не указана цена"},
		},
	}
}

func newImportHandlers(t *testing.T, imp *fakeImporter) *handlers {
	t.Helper()
	return &handlers{render: newTestRenderer(t), importer: imp, importTokenKey: []byte("test-key")}
}

// importRequest builds a multipart POST like the import form sends.
func importRequest(t *testing.T, target, file, pointID, token string, htmx bool) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if file != "" {
		fw, err := mw.CreateFormFile("file", "goods.csv")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte(file))
	}
	_ = mw.WriteField("point_id", pointID)
	if token != "" {
		_ = mw.WriteField("checked_token", token)
	}
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, target, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if htmx {
		r.Header.Set("HX-Request", "true")
	}
	return r.WithContext(staff.NewContextWithStaff(r.Context(), manager()))
}

var (
	runButtonRE = regexp.MustCompile(`(?s)<button[^>]*id="import-run"[^>]*>`)
	tokenRE     = regexp.MustCompile(`name="checked_token" value="([^"]+)"`)
)

func runButton(t *testing.T, body string) string {
	t.Helper()
	m := runButtonRE.FindString(body)
	if m == "" {
		t.Fatalf("no import-run button in:\n%s", body)
	}
	return m
}

func TestImportPageStartsWithRunDisabled(t *testing.T) {
	// Arrange
	imp := &fakeImporter{points: []catalog.ImportPoint{{ID: "pt1", Name: "Дордой"}}}
	h := newImportHandlers(t, imp)
	r := requestAs(http.MethodGet, "/admin/products/import", manager(), nil)
	w := httptest.NewRecorder()

	// Act
	h.productImportPage(w, r)

	// Assert
	body := w.Body.String()
	if !strings.Contains(runButton(t, body), "disabled") {
		t.Errorf("run button must start disabled: %s", runButton(t, body))
	}
	if !strings.Contains(body, `hx-post="/admin/products/import/check"`) || !strings.Contains(body, `value="pt1"`) {
		t.Errorf("page lacks the check form or the server-rendered points")
	}
}

func TestImportCheckWithoutErrorsEnablesRun(t *testing.T) {
	// Arrange
	imp := &fakeImporter{result: cleanImportResult()}
	h := newImportHandlers(t, imp)
	w := httptest.NewRecorder()

	// Act
	h.productImportCheck(w, importRequest(t, "/admin/products/import/check", "name_ru\nКеды\n", "pt1", "", true))

	// Assert
	if len(imp.calls) != 1 || !imp.calls[0].DryRun || imp.calls[0].PointID != "pt1" {
		t.Fatalf("calls = %+v, want one dry run for pt1", imp.calls)
	}
	body := w.Body.String()
	btn := runButton(t, body)
	if strings.Contains(btn, "disabled") {
		t.Errorf("run button still disabled after a clean check: %s", btn)
	}
	if !strings.Contains(body, `hx-swap-oob="true"`) || !tokenRE.MatchString(body) {
		t.Errorf("check response must swap in the run slot with a token:\n%s", body)
	}
}

func TestImportCheckWithErrorsKeepsRunDisabled(t *testing.T) {
	// Arrange
	imp := &fakeImporter{result: erroredImportResult()}
	h := newImportHandlers(t, imp)
	w := httptest.NewRecorder()

	// Act
	h.productImportCheck(w, importRequest(t, "/admin/products/import/check", "x", "", "", true))

	// Assert
	body := w.Body.String()
	if !strings.Contains(runButton(t, body), "disabled") || tokenRE.MatchString(body) {
		t.Errorf("run must stay disabled without a token when the check found errors:\n%s", body)
	}
	if !strings.Contains(body, "Не указана цена") {
		t.Errorf("report rows not rendered:\n%s", body)
	}
}

func TestImportApplyRunsOnlyTheCheckedFile(t *testing.T) {
	// Arrange: a clean check of file A issues a token.
	imp := &fakeImporter{result: cleanImportResult()}
	h := newImportHandlers(t, imp)
	w := httptest.NewRecorder()
	h.productImportCheck(w, importRequest(t, "/admin/products/import/check", "A", "pt1", "", true))
	token := tokenRE.FindStringSubmatch(w.Body.String())[1]

	cases := []struct {
		name, file, point, token string
		wantApplied              bool
	}{
		{"no token", "A", "pt1", "", false},
		{"forged token", "A", "pt1", "deadbeef", false},
		{"other file", "B", "pt1", token, false},
		{"other point", "A", "pt2", token, false},
		{"checked file", "A", "pt1", token, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			imp.calls = nil
			w := httptest.NewRecorder()

			// Act
			h.productImportApply(w, importRequest(t, "/admin/products/import/apply", c.file, c.point, c.token, true))

			// Assert
			applied := len(imp.calls) == 1 && !imp.calls[0].DryRun
			if applied != c.wantApplied {
				t.Fatalf("applied = %v (calls %+v), want %v", applied, imp.calls, c.wantApplied)
			}
			body := w.Body.String()
			if !strings.Contains(runButton(t, body), "disabled") {
				t.Errorf("run must be disabled again after an apply attempt")
			}
			if !c.wantApplied && !strings.Contains(body, ruTr.T("admin.import.err_recheck")) {
				t.Errorf("rejected apply must ask to check again:\n%s", body)
			}
		})
	}
}

func TestImportResetDisablesRun(t *testing.T) {
	h := newImportHandlers(t, &fakeImporter{})
	r := requestAs(http.MethodGet, "/admin/products/import/reset", manager(), nil)
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()

	h.productImportReset(w, r)

	if !strings.Contains(runButton(t, w.Body.String()), "disabled") {
		t.Errorf("reset must render a disabled run button: %s", w.Body.String())
	}
}

func TestImportCheckWithoutHTMXRendersFullPage(t *testing.T) {
	// Arrange: no JS — the form posts normally.
	imp := &fakeImporter{result: cleanImportResult()}
	h := newImportHandlers(t, imp)
	w := httptest.NewRecorder()

	// Act
	h.productImportCheck(w, importRequest(t, "/admin/products/import/check", "A", "", "", false))

	// Assert: the report, but no import — a full-page reply can't keep
	// the file (see TestImportPageHidesRunWithoutJS).
	body := w.Body.String()
	if !strings.Contains(body, "<html") || !strings.Contains(runButton(t, body), "disabled") {
		t.Errorf("want the whole page with the run button disabled:\n%s", body)
	}
}

func TestImportCheckRejectsMissingFile(t *testing.T) {
	imp := &fakeImporter{result: cleanImportResult()}
	h := newImportHandlers(t, imp)
	w := httptest.NewRecorder()

	h.productImportCheck(w, importRequest(t, "/admin/products/import/check", "", "", "", true))

	if len(imp.calls) != 0 || !strings.Contains(w.Body.String(), ruTr.T("admin.import.err_no_file")) {
		t.Errorf("calls = %d, body:\n%s", len(imp.calls), w.Body.String())
	}
}

func TestImportCheckFileErrorInAdminLanguage(t *testing.T) {
	// Arrange: the importer rejects the file (missing required columns).
	fileErr := apperr.BadRequest("missing_columns", "в файле нет обязательных колонок: Цена (price) — скачайте шаблон импорта").
		WithParams(map[string]string{"columns": "Цена (price)"})
	imp := &fakeImporter{err: fileErr}
	h := newImportHandlers(t, imp)
	r := importRequest(t, importCheckPath, "A", "", "", true)
	r = r.WithContext(contextWithLang(r.Context(), i18n.LangKY))
	w := httptest.NewRecorder()

	// Act
	h.productImportCheck(&langWriter{ResponseWriter: w, lang: i18n.LangKY}, r)

	// Assert
	want := i18n.UpperFirst(apperr.Localize(i18n.LangKY, fileErr))
	if body := w.Body.String(); !strings.Contains(body, want) || !strings.Contains(runButton(t, body), "disabled") {
		t.Errorf("want %q and a disabled run button:\n%s", want, body)
	}
}
