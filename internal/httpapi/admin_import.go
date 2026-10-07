package httpapi

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/importguard"
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
//
// guard is the single importguard.Guard also given to admin.RegisterRoutes
// (fix/json-import-guards): the upload shares the HTML page's per-staff
// rate limit and concurrency cap, so switching endpoints buys nothing.
// nil = unlimited (tests only).
func RegisterAdminImportRoutes(mux *http.ServeMux, db *sql.DB, staffSvc *staff.Service, guard *importguard.Guard) {
	store := catalog.NewSQLImportStore(db)
	managerOnly := staffSvc.RequireRole(staff.RoleOwner, staff.RoleManager)
	mux.Handle("POST /admin/products/import", managerOnly(apperr.Wrap(importProductsHandler(store, guard))))
	mux.Handle("GET /admin/products/import/template", managerOnly(apperr.Wrap(importTemplateHandler(store))))
	mux.Handle("GET /admin/products/import/points", managerOnly(apperr.Wrap(importPointsHandler(store))))
}

// importProductsHandler extracts the uploaded file, detects its format and
// runs catalog.ImportProducts. Per-row problems are part of the 200
// response; only an unusable request/file is an HTTP error. Both come in
// the request's language (apperr.LangFromRequest: the import page sends
// its own via Accept-Language, and Accept: application/json so file
// errors are JSON rather than an HTML error page).
//
// Before the body is read: a request without a staff ID is refused (403),
// then guard is consulted — out of tokens or no free slot is a 429 with
// Retry-After.
func importProductsHandler(store catalog.ImportStore, guard *importguard.Guard) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		release, err := beginJSONImport(w, r, guard)
		if err != nil {
			return err
		}
		defer release()

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
			Lang:    apperr.LangFromRequest(r),
		})
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, result)
	}
}

// beginJSONImport identifies the staff member and passes the request
// through guard. On success the caller must defer release; on a refusal
// the returned *apperr.AppError (with Retry-After already set for a 429)
// is the response.
func beginJSONImport(w http.ResponseWriter, r *http.Request, guard *importguard.Guard) (release func(), err error) {
	st, ok := staff.FromContext(r.Context())
	if !ok || st == nil || st.ID == "" {
		// RequireRole should make this impossible; an empty ID would put
		// every such request in one shared rate-limit bucket.
		slog.ErrorContext(r.Context(), "admin JSON import without a staff ID in context")
		return nil, apperr.Forbidden("forbidden", "недостаточно прав для этого действия")
	}
	d := guard.Begin(r.Context(), st.ID)
	switch d.Outcome {
	case importguard.Allowed:
		return d.Release, nil
	case importguard.RateLimited:
		slog.WarnContext(r.Context(), "admin JSON import rate limited", "staff_id", st.ID, "retry_after", d.RetryAfter)
		w.Header().Set("Retry-After", importguard.RetryAfterSeconds(d.RetryAfter))
		return nil, apperr.TooManyRequests("import_rate_limited", "слишком много проверок и импортов подряд — подождите минуту и попробуйте снова")
	default:
		slog.WarnContext(r.Context(), "admin JSON import busy", "max_concurrent", guard.Gate.Cap())
		w.Header().Set("Retry-After", importguard.RetryAfterSeconds(d.RetryAfter))
		return nil, apperr.TooManyRequests("import_busy", "сейчас уже идут другие проверки или импорты — подождите несколько секунд и попробуйте снова")
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
		data, err := catalog.BuildImportTemplate(adminRequestLang(r), cats)
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
