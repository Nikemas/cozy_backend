package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
)

// --- fake import backend: an empty catalog, every write succeeds ---

type fakeImportBackend struct {
	products, variants int
	dryRuns            int
	pointIDs           []string
}

func (f *fakeImportBackend) ResolveCategory(_ context.Context, raw string) (string, error) {
	if raw == "sneakers" || raw == "Кроссовки" {
		return "cat-1", nil
	}
	return "", apperr.BadRequest("category_not_found", "категория не найдена")
}

func (f *fakeImportBackend) ActivePoint(_ context.Context, id string) (bool, error) {
	return id == testImportPoint, nil
}

func (f *fakeImportBackend) DefaultPoint(context.Context) (string, error) {
	return testImportPoint, nil
}

func (f *fakeImportBackend) Begin(_ context.Context, dryRun bool) (catalog.ImportSession, error) {
	if dryRun {
		f.dryRuns++
	}
	return fakeImportSession{f}, nil
}

func (f *fakeImportBackend) ActivePoints(context.Context) ([]catalog.ImportPoint, error) {
	return []catalog.ImportPoint{{ID: testImportPoint, Name: "ЦУМ", Address: "пр. Чуй"}}, nil
}

func (f *fakeImportBackend) TemplateCategories(context.Context) ([]catalog.TemplateCategory, error) {
	return []catalog.TemplateCategory{{Slug: "sneakers", NameRu: "Кроссовки", NameKy: "Кроссовкалар"}}, nil
}

const testImportPoint = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

type fakeImportSession struct{ f *fakeImportBackend }

func (s fakeImportSession) Model(_ context.Context, fn func(catalog.ImportTx) error) error {
	return fn(fakeImportTx(s))
}
func (s fakeImportSession) Close() error { return nil }

type fakeImportTx struct{ f *fakeImportBackend }

func (fakeImportTx) ProductByModelCode(context.Context, string) (string, error) { return "", nil }
func (fakeImportTx) ProductsByName(context.Context, string, string, string, string) ([]string, error) {
	return nil, nil
}
func (t fakeImportTx) CreateProduct(context.Context, catalog.ImportProduct) (string, error) {
	t.f.products++
	return fmt.Sprintf("product-%d", t.f.products), nil
}
func (fakeImportTx) UpdateProduct(context.Context, string, catalog.ImportProduct) error { return nil }
func (fakeImportTx) VariantBySKU(context.Context, string) (*catalog.ImportVariantRef, error) {
	return nil, nil
}
func (fakeImportTx) VariantBySizeColor(context.Context, string, string, string) (*catalog.ImportVariantRef, error) {
	return nil, nil
}
func (t fakeImportTx) CreateVariant(context.Context, string, catalog.ImportVariant) (string, error) {
	t.f.variants++
	return fmt.Sprintf("variant-%d", t.f.variants), nil
}
func (fakeImportTx) UpdateVariant(context.Context, string, catalog.ImportVariant) error { return nil }
func (t fakeImportTx) SetStock(_ context.Context, _, pointID string, _ int) error {
	t.f.pointIDs = append(t.f.pointIDs, pointID)
	return nil
}

func newImportDeps() *fakeImportBackend { return &fakeImportBackend{} }

// newImportRequest builds a multipart/form-data POST with a single "file"
// part. When filename == "" and body == "", no part is added at all (used
// to test the "missing file field" case). When contentType != "", the part
// gets an explicit Content-Type header (mirroring what a browser attaches
// to the upload) instead of relying on multipart.Writer's default.
func newImportRequest(t *testing.T, filename, contentType, body string) *http.Request {
	t.Helper()
	return newImportRequestWithFields(t, filename, contentType, body, nil)
}

// newImportRequestWithFields also adds plain form fields (dry_run, point_id).
func newImportRequestWithFields(t *testing.T, filename, contentType, body string, fields map[string]string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	}

	if filename != "" {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
		if contentType != "" {
			h.Set("Content-Type", contentType)
		}
		part, err := mw.CreatePart(h)
		if err != nil {
			t.Fatalf("CreatePart: %v", err)
		}
		if _, err := io.WriteString(part, body); err != nil {
			t.Fatalf("write part body: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/products/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestImportProductsHandlerMissingFile(t *testing.T) {
	handler := apperr.Wrap(importProductsHandler(newImportDeps()))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newImportRequest(t, "", "", ""))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportProductsHandlerUnsupportedFormat(t *testing.T) {
	handler := apperr.Wrap(importProductsHandler(newImportDeps()))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newImportRequest(t, "products.txt", "text/plain", "whatever"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportProductsHandlerRejectsUnrecognizedFormatBeforeParsing(t *testing.T) {
	// A body that would fail CSV/XLSX parsing outright, on a file whose
	// name/content-type isn't recognized at all — the handler must reject
	// on format alone, before ever handing this to catalog.ImportProducts.
	handler := apperr.Wrap(importProductsHandler(newImportDeps()))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newImportRequest(t, "products.bin", "application/octet-stream", "\x00\x01garbage"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportProductsHandlerCSVExtensionHappyPath(t *testing.T) {
	handler := apperr.Wrap(importProductsHandler(newImportDeps()))

	csvBody := "name_ru,name_ky,category,price\n" +
		"Кроссовки,Кроссовкалар,sneakers,4999\n"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newImportRequest(t, "products.csv", "", csvBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result catalog.ImportResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Imported != 1 {
		t.Errorf("Imported = %d, want 1", result.Imported)
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors = %+v, want none", result.Errors)
	}
}

func TestImportProductsHandlerContentTypeFallbackWhenNoExtension(t *testing.T) {
	handler := apperr.Wrap(importProductsHandler(newImportDeps()))

	csvBody := "name_ru,name_ky,category,price\n" +
		"Кроссовки,Кроссовкалар,sneakers,4999\n"

	// No file extension at all — format must be recognized from the
	// declared Content-Type instead.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newImportRequest(t, "upload", "text/csv", csvBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportProductsHandlerReportsPerRowErrorsWithHTTP200(t *testing.T) {
	// The request as a whole succeeded (the file was read and processed);
	// per-row failures are data in the response body, not an HTTP error —
	// exactly the "one bad row doesn't kill the batch" contract.
	handler := apperr.Wrap(importProductsHandler(newImportDeps()))

	csvBody := "name_ru,name_ky,category,price\n" +
		"OK,ОК,sneakers,1000\n" +
		"Bad,ОК2,unknown-category,1000\n"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newImportRequest(t, "products.csv", "", csvBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result catalog.ImportResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Imported != 1 {
		t.Errorf("Imported = %d, want 1", result.Imported)
	}
	if len(result.Errors) != 1 || result.Errors[0].Row != 3 {
		t.Errorf("Errors = %+v, want exactly 1 at row 3", result.Errors)
	}
}

func TestImportProductsHandlerWholeFileFailureIsHTTPError(t *testing.T) {
	// Unlike a bad data row, a structurally malformed file can't be
	// processed at all — the handler must surface that as an HTTP error,
	// not a 200 with an empty result.
	handler := apperr.Wrap(importProductsHandler(newImportDeps()))

	badCSV := "name_ru,name_ky,category,price\n" +
		"Foo\"Bar,ОК,sneakers,4999\n"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newImportRequest(t, "products.csv", "", badCSV))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportProductsHandlerDryRunAndPoint(t *testing.T) {
	backend := newImportDeps()
	handler := apperr.Wrap(importProductsHandler(backend))

	csvBody := "Артикул,Название,Категория,Цена,Размер,Цвет,Остаток\n" +
		"A1,Кеды,sneakers,1000,38,red,2\n" +
		"A1,,,,39,red,1\n"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newImportRequestWithFields(t, "p.csv", "", csvBody,
		map[string]string{"dry_run": "1", "point_id": testImportPoint}))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result catalog.ImportResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || backend.dryRuns != 1 {
		t.Errorf("dry run not propagated: result=%v begins=%d", result.DryRun, backend.dryRuns)
	}
	if result.Summary.ProductsCreated != 1 || result.Summary.VariantsCreated != 2 || len(result.Rows) != 2 {
		t.Errorf("summary = %+v rows=%+v", result.Summary, result.Rows)
	}
	if result.PointID == nil || *result.PointID != testImportPoint || len(backend.pointIDs) != 2 {
		t.Errorf("point = %v, stock writes = %v", result.PointID, backend.pointIDs)
	}

	// An unknown point is a request error, not a per-row one.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, newImportRequestWithFields(t, "p.csv", "", csvBody,
		map[string]string{"point_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown point: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportTemplateHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	apperr.Wrap(importTemplateHandler(newImportDeps())).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/admin/products/import/template", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="cozy_import_template.xlsx"` {
		t.Errorf("Content-Disposition = %q", cd)
	}

	// The template round-trips through the importer without errors.
	res, err := catalog.ImportProducts(context.Background(), bytes.NewReader(rec.Body.Bytes()),
		catalog.ImportFormatXLSX, newImportDeps(), catalog.ImportOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Errors != 0 || res.Summary.Skipped != 0 || res.Summary.Rows == 0 {
		t.Errorf("template import summary = %+v rows=%+v", res.Summary, res.Rows)
	}
}

func TestImportPointsHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	apperr.Wrap(importPointsHandler(newImportDeps())).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/admin/products/import/points", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body struct {
		Points []catalog.ImportPoint `json:"points"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Points) != 1 || body.Points[0].ID != testImportPoint {
		t.Errorf("points = %+v", body.Points)
	}
}
