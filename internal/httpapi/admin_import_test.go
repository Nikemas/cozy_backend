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

// --- fakes wired into catalog.ImportDeps for end-to-end handler tests
// without a live database ---

type fakeImportCategoryResolver struct{ known map[string]string }

func (f *fakeImportCategoryResolver) ResolveID(_ context.Context, idOrSlug string) (string, error) {
	if id, ok := f.known[idOrSlug]; ok {
		return id, nil
	}
	return "", apperr.NotFound("category_not_found", "категория не найдена")
}

type fakeImportProductCreator struct{ n int }

func (f *fakeImportProductCreator) Create(_ context.Context, in catalog.ProductInput) (*catalog.Product, error) {
	f.n++
	return &catalog.Product{ID: fmt.Sprintf("product-%d", f.n), CategoryID: in.CategoryID, NameRu: in.NameRu, NameKy: in.NameKy, BasePrice: in.BasePrice}, nil
}

type fakeImportVariantCreator struct{ n int }

func (f *fakeImportVariantCreator) Create(_ context.Context, productID string, in catalog.VariantInput) (*catalog.Variant, error) {
	f.n++
	return &catalog.Variant{ID: fmt.Sprintf("variant-%d", f.n), ProductID: productID, Size: in.Size, Color: in.Color}, nil
}

func newImportDeps() catalog.ImportDeps {
	return catalog.ImportDeps{
		Categories: &fakeImportCategoryResolver{known: map[string]string{"sneakers": "cat-1"}},
		Products:   &fakeImportProductCreator{},
		Variants:   &fakeImportVariantCreator{},
		// Stock left nil: these handler-level tests exercise multipart
		// extraction, format detection and result propagation — the stock
		// column behavior itself is covered exhaustively in
		// internal/catalog/import_test.go.
	}
}

// newImportRequest builds a multipart/form-data POST with a single "file"
// part. When filename == "" and body == "", no part is added at all (used
// to test the "missing file field" case). When contentType != "", the part
// gets an explicit Content-Type header (mirroring what a browser attaches
// to the upload) instead of relying on multipart.Writer's default.
func newImportRequest(t *testing.T, filename, contentType, body string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

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
