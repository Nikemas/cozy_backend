package httpapi

import (
	"database/sql"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// maxImportUploadSize bounds how much of an incoming multipart request this
// handler will read into memory: large enough for a realistic product
// spreadsheet, small enough that a mis-sized or malicious upload can't
// exhaust server memory before format detection has even run.
const maxImportUploadSize = 20 << 20 // 20 MiB

// RegisterAdminImportRoutes mounts POST /admin/products/import — bulk
// product import from a CSV or Excel file, per §8 of the ТЗ. This path is
// deliberately NOT under /admin/api/, unlike every other admin write
// endpoint in this codebase — that's what the ТЗ and the Task acceptance
// criteria both call for, so it's kept as-is rather than "corrected" to
// match the rest of the admin API's prefix.
//
// Not wired into cmd/server/main.go by this change — see the accompanying
// task notes: registerAdminRoutes is under heavy concurrent edit from other
// Wave 3 work right now, so leaving the one-line call for whoever merges
// next avoids adding a collision point to an already busy function.
func RegisterAdminImportRoutes(mux *http.ServeMux, db *sql.DB, staffSvc *staff.Service) {
	deps := catalog.ImportDeps{
		Categories: catalog.NewCategoryRepo(db),
		Products:   catalog.NewProductRepo(db),
		Variants:   catalog.NewVariantRepo(db),
		Stock:      catalog.NewStockRepo(db),
	}

	managerOnly := staffSvc.RequireRole(staff.RoleOwner, staff.RoleManager)
	mux.Handle("POST /admin/products/import", managerOnly(apperr.Wrap(importProductsHandler(deps))))
}

// importProductsHandler extracts the uploaded file from a multipart
// request, detects its format from the filename extension and/or
// Content-Type, and delegates the actual parsing/validation/import to
// catalog.ImportProducts. It takes deps (rather than concrete *catalog.*
// repo types) so tests can drive it end-to-end with fakes, without a live
// database.
func importProductsHandler(deps catalog.ImportDeps) apperr.HandlerFunc {
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

		// Detected — and rejected, if unrecognized — before any parsing is
		// attempted, per the task's acceptance criteria.
		format, ok := catalog.DetectImportFormat(header.Filename, header.Header.Get("Content-Type"))
		if !ok {
			return apperr.BadRequest("unsupported_format", "поддерживаются только файлы .csv и .xlsx")
		}

		result, err := catalog.ImportProducts(r.Context(), file, format, deps)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, result)
	}
}
