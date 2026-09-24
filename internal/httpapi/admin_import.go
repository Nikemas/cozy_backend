package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// maxImportUploadSize bounds how much of an incoming multipart request this
// handler will read into memory: large enough for a realistic product
// spreadsheet, small enough that a mis-sized or malicious upload can't
// exhaust server memory before format detection has even run.
const maxImportUploadSize = 20 << 20 // 20 MiB

// importBackend is everything the import endpoints need from the
// database; *catalog.SQLImportStore implements it, tests use a fake.
type importBackend interface {
	catalog.ImportStore
	ActivePoints(ctx context.Context) ([]catalog.ImportPoint, error)
	TemplateCategories(ctx context.Context) ([]catalog.TemplateCategory, error)
}

// RegisterAdminImportRoutes mounts the bulk product import (§8 of the ТЗ),
// owner/manager only:
//
//	POST /admin/products/import          — multipart: file (.csv/.xlsx),
//	                                       dry_run (1 = check only), point_id
//	GET  /admin/products/import/template — the .xlsx template
//	GET  /admin/products/import/points   — active points for the stock target
//
// Deliberately NOT under /admin/api/, unlike the other admin JSON
// endpoints — that's the path the ТЗ names. The HTML page itself
// (GET /admin/products/import) lives in internal/admin.
func RegisterAdminImportRoutes(mux *http.ServeMux, db *sql.DB, staffSvc *staff.Service) {
	store := catalog.NewSQLImportStore(db)
	managerOnly := staffSvc.RequireRole(staff.RoleOwner, staff.RoleManager)
	mux.Handle("POST /admin/products/import", managerOnly(apperr.Wrap(importProductsHandler(store))))
	mux.Handle("GET /admin/products/import/template", managerOnly(apperr.Wrap(importTemplateHandler(store))))
	mux.Handle("GET /admin/products/import/points", managerOnly(apperr.Wrap(importPointsHandler(store))))
}

// importProductsHandler extracts the uploaded file, detects its format and
// runs catalog.ImportProducts. Per-row problems are part of the 200
// response; only an unusable request/file is an HTTP error.
func importProductsHandler(store catalog.ImportStore) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		r.Body = http.MaxBytesReader(w, r.Body, maxImportUploadSize)
		if err := r.ParseMultipartForm(maxImportUploadSize); err != nil {
			return apperr.BadRequest("invalid_multipart", "некорректная multipart-форма или файл слишком большой")
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			return apperr.BadRequest("missing_file", "поле file обязательно")
		}
		defer func() { _ = file.Close() }()

		// Detected — and rejected, if unrecognized — before any parsing.
		format, ok := catalog.DetectImportFormat(header.Filename, header.Header.Get("Content-Type"))
		if !ok {
			return apperr.BadRequest("unsupported_format", "поддерживаются только файлы .csv и .xlsx")
		}

		dryRun, _ := strconv.ParseBool(r.FormValue("dry_run"))
		result, err := catalog.ImportProducts(r.Context(), file, format, store, catalog.ImportOptions{
			DryRun:  dryRun,
			PointID: r.FormValue("point_id"),
		})
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, result)
	}
}

// importTemplateHandler serves the .xlsx template, with the current
// categories on a reference sheet.
func importTemplateHandler(b importBackend) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		cats, err := b.TemplateCategories(r.Context())
		if err != nil {
			return err
		}
		data, err := catalog.BuildImportTemplate(cats)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", `attachment; filename="cozy_import_template.xlsx"`)
		w.Header().Set("Cache-Control", "no-store")
		_, err = w.Write(data)
		return err
	}
}

// importPointsHandler lists active points of sale; the first one is the
// default stock target.
func importPointsHandler(b importBackend) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		points, err := b.ActivePoints(r.Context())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, map[string]any{"points": points})
	}
}
