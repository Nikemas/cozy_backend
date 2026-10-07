// product_import.go (fix/admin-polish): the Товары → Импорт screen as a
// server-rendered htmx flow. «Проверить» posts the file for a dry run and
// gets the report back as HTML; «Импортировать» is rendered disabled until
// a check of that exact file (and stock point) came back without errors —
// the check response swaps in an enabled button carrying a signed token
// of the checked file, and the apply handler refuses any upload whose
// token doesn't match. Without JS the same form posts normally and the
// whole page comes back in the new state.
//
// The JSON endpoints in internal/httpapi/admin_import.go stay for API
// clients; both call catalog.ImportProducts.
package admin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// Import screen URLs.
const (
	importBasePath  = "/admin/products/import"
	importCheckPath = importBasePath + "/check"
	importApplyPath = importBasePath + "/apply"
	importResetPath = importBasePath + "/reset"
)

// maxImportUploadBytes matches the JSON endpoint's limit (httpapi).
const maxImportUploadBytes = 20 << 20

// importTokenKeyBytes is the size of the per-process token signing key.
const importTokenKeyBytes = 32

// productImporter is what the import screen needs: running an import
// (dry or real) and listing the stock points to choose from.
type productImporter interface {
	Import(ctx context.Context, r io.Reader, format catalog.ImportFormat, opts catalog.ImportOptions) (*catalog.ImportResult, error)
	ActivePoints(ctx context.Context) ([]catalog.ImportPoint, error)
}

// sqlProductImporter is the production productImporter.
type sqlProductImporter struct{ store *catalog.SQLImportStore }

func newSQLProductImporter(db *sql.DB) *sqlProductImporter {
	return &sqlProductImporter{store: catalog.NewSQLImportStore(db)}
}

func (s *sqlProductImporter) Import(ctx context.Context, r io.Reader, format catalog.ImportFormat, opts catalog.ImportOptions) (*catalog.ImportResult, error) {
	return catalog.ImportProducts(ctx, r, format, s.store, opts)
}

func (s *sqlProductImporter) ActivePoints(ctx context.Context) ([]catalog.ImportPoint, error) {
	return s.store.ActivePoints(ctx)
}

// newImportTokenKey returns a random key for signing check tokens. A
// restart invalidates outstanding tokens; the page then just asks for
// another check.
func newImportTokenKey() []byte {
	key := make([]byte, importTokenKeyBytes)
	if _, err := rand.Read(key); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return key
}

// importUpload is one parsed POST of the import form.
type importUpload struct {
	data     []byte
	format   catalog.ImportFormat
	pointID  string
	token    string
	staffID  string
	errorKey string // locale key of a form-level problem, "" if none
}

// readImportUpload parses the multipart form; problems are reported in the
// returned upload, never as a Go error.
func readImportUpload(w http.ResponseWriter, r *http.Request) importUpload {
	var up importUpload
	if st, ok := staff.FromContext(r.Context()); ok {
		up.staffID = st.ID
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImportUploadBytes)
	if err := r.ParseMultipartForm(maxImportUploadBytes); err != nil {
		up.errorKey = "admin.import.err_upload"
		return up
	}
	up.pointID = r.FormValue("point_id")
	up.token = r.FormValue("checked_token")
	file, header, err := r.FormFile("file")
	if err != nil {
		up.errorKey = "admin.import.err_no_file"
		return up
	}
	defer func() { _ = file.Close() }()
	format, ok := catalog.DetectImportFormat(header.Filename, header.Header.Get("Content-Type"))
	if !ok {
		up.errorKey = "admin.import.err_format"
		return up
	}
	data, err := io.ReadAll(file)
	if err != nil {
		up.errorKey = "admin.import.err_upload"
		return up
	}
	up.data, up.format = data, format
	return up
}

// failed reports whether the upload can't be imported at all.
func (up importUpload) failed() bool { return up.errorKey != "" }

// importToken signs what was checked: the file, the stock point and who
// checked it.
func importToken(key []byte, up importUpload) string {
	sum := sha256.Sum256(up.data)
	mac := hmac.New(sha256.New, key)
	mac.Write(sum[:])
	mac.Write([]byte{0})
	mac.Write([]byte(up.pointID))
	mac.Write([]byte{0})
	mac.Write([]byte(up.staffID))
	return hex.EncodeToString(mac.Sum(nil))
}

func validImportToken(key []byte, up importUpload) bool {
	if up.token == "" {
		return false
	}
	return hmac.Equal([]byte(up.token), []byte(importToken(key, up)))
}

// blocksImport reports whether a dry run's result must keep «Импортировать»
// disabled: any error row (skipped rows are models failing on such a row)
// or nothing to import.
func blocksImport(res *catalog.ImportResult) bool {
	return res == nil || res.Summary.Errors > 0 || res.Summary.Skipped > 0 || res.Summary.Rows == 0
}

// productImportPage handles GET /admin/products/import.
func (h *handlers) productImportPage(w http.ResponseWriter, r *http.Request) {
	h.renderImportPage(w, r, importBaseData())
}

// productImportCheck handles POST /admin/products/import/check: a dry run.
func (h *handlers) productImportCheck(w http.ResponseWriter, r *http.Request) {
	t := h.tr(r)
	up := readImportUpload(w, r)
	data := importBaseData()
	data.PointID = up.pointID
	if up.failed() {
		data.Result = &ImportResultVM{Error: t.T(up.errorKey)}
		h.respondImport(w, r, data)
		return
	}
	res, err := h.importer.Import(r.Context(), bytes.NewReader(up.data), up.format,
		catalog.ImportOptions{DryRun: true, PointID: up.pointID, Lang: t.Lang()})
	if err != nil {
		slog.WarnContext(r.Context(), "admin import check failed", "err", err)
		data.Result = &ImportResultVM{Error: importErrMessage(t, err)}
		h.respondImport(w, r, data)
		return
	}
	data.Result = buildImportResultVM(t, res)
	if !blocksImport(res) {
		data.Run = ImportRunVM{Enabled: true, Token: importToken(h.importTokenKey, up)}
	}
	h.respondImport(w, r, data)
}

// productImportApply handles POST /admin/products/import/apply: the real
// import, only for the file a clean check signed.
func (h *handlers) productImportApply(w http.ResponseWriter, r *http.Request) {
	t := h.tr(r)
	up := readImportUpload(w, r)
	data := importBaseData()
	data.PointID = up.pointID
	switch {
	case up.failed():
		data.Result = &ImportResultVM{Error: t.T(up.errorKey)}
	case !validImportToken(h.importTokenKey, up):
		data.Result = &ImportResultVM{Error: t.T("admin.import.err_recheck")}
	default:
		res, err := h.importer.Import(r.Context(), bytes.NewReader(up.data), up.format,
			catalog.ImportOptions{PointID: up.pointID, Lang: t.Lang()})
		if err != nil {
			slog.ErrorContext(r.Context(), "admin import failed", "err", err)
			data.Result = &ImportResultVM{Error: importErrMessage(t, err)}
		} else {
			data.Result = buildImportResultVM(t, res)
		}
	}
	h.respondImport(w, r, data)
}

// productImportReset handles GET /admin/products/import/reset — sent when
// another file is chosen: clears the report and disables «Импортировать».
func (h *handlers) productImportReset(w http.ResponseWriter, r *http.Request) {
	h.respondImport(w, r, importBaseData())
}

// importErrMessage is a file-level import error (missing columns, an
// unreadable file, ...) in t's language: catalog raises them as apperr
// codes translated in locales/errors.*.yaml, with their parameters.
func importErrMessage(t tr, err error) string {
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		return i18n.UpperFirst(apperr.Localize(t.Lang(), ae))
	}
	return t.T("admin.err.generic")
}

func importBaseData() ImportPageData {
	return ImportPageData{
		ImportURL: importBasePath, CheckURL: importCheckPath,
		ApplyURL: importApplyPath, ResetURL: importResetPath,
	}
}

// respondImport sends the htmx fragment (report + out-of-band run button)
// to an htmx request, the whole page otherwise.
func (h *handlers) respondImport(w http.ResponseWriter, r *http.Request, data ImportPageData) {
	if r.Header.Get("HX-Request") != "true" {
		h.renderImportPage(w, r, data)
		return
	}
	if err := h.render.RenderScreenFragment(w, "product_import", "import_response", data); err != nil {
		slog.ErrorContext(r.Context(), "admin import fragment render failed", "err", err)
		http.Error(w, h.tr(r).T("admin.err.render"), http.StatusInternalServerError)
	}
}

func (h *handlers) renderImportPage(w http.ResponseWriter, r *http.Request, data ImportPageData) {
	st, _ := staff.FromContext(r.Context())
	data.Points, data.PointsFailed = h.importPoints(r.Context())
	pageData := h.productsShellData("product_import", "admin.import.title", st)
	pageData.ShowBack = true
	pageData.BackURL = productsListPath
	pageData.Data = data
	if err := h.render.Render(w, "product_import", pageData); err != nil {
		http.Error(w, h.tr(r).T("admin.err.render"), http.StatusInternalServerError)
	}
}

// importPoints lists the stock point choices; failed is true when they
// could not be loaded (the form then offers only the default point).
func (h *handlers) importPoints(ctx context.Context) (points []catalog.ImportPoint, failed bool) {
	if h.importer == nil {
		return nil, true
	}
	points, err := h.importer.ActivePoints(ctx)
	if err != nil {
		slog.WarnContext(ctx, "admin import: loading points failed", "err", err)
		return nil, true
	}
	return points, false
}

// buildImportResultVM turns an import result into the report shown under
// the form (dry run: "будет создано", real run: "создано").
func buildImportResultVM(t tr, res *catalog.ImportResult) *ImportResultVM {
	s := res.Summary
	vm := &ImportResultVM{DryRun: res.DryRun, HasProblems: s.Errors > 0 || s.Skipped > 0}
	created, updated, skipped := "admin.import.st_created", "admin.import.st_updated", "admin.import.st_skipped"
	if res.DryRun {
		created, updated, skipped = "admin.import.st_will_create", "admin.import.st_will_update", "admin.import.st_will_skip"
	}
	switch {
	case res.DryRun && vm.HasProblems:
		vm.Head = t.T("admin.import.dry_errors")
	case res.DryRun:
		vm.Head = t.T("admin.import.dry_ok")
	default:
		n := s.ProductsCreated + s.ProductsUpdated
		vm.Head = t.T("admin.import.imported") + ": " + t.N(n, "admin.plural.product")
	}
	vm.Summary = t.F("admin.import.summary_line",
		t.T("admin.nav.products"), t.T(created), s.ProductsCreated, t.T(updated), s.ProductsUpdated,
		t.T("admin.product.variants"), t.T(created), s.VariantsCreated, t.T(updated), s.VariantsUpdated,
		t.T("admin.import.rows"), s.Rows, t.T("admin.import.with_errors"), s.Errors, t.T(skipped), s.Skipped)

	var bad []string
	for _, row := range res.Rows {
		vm.Rows = append(vm.Rows, importRowVM(t, row, res.DryRun))
		if row.Status == catalog.RowStatusError || row.Status == catalog.RowStatusSkipped {
			bad = append(bad, importBadRowLine(t, row))
		}
	}
	if len(bad) > 0 {
		title := "admin.import.not_imported"
		if res.DryRun {
			title = "admin.import.will_not"
		}
		vm.BadTitle = t.T(title) + ": " + t.N(len(bad), "admin.plural.row")
		vm.BadRows = bad
	}
	return vm
}

func importRowVM(t tr, row catalog.ImportRowResult, dry bool) ImportRowVM {
	return ImportRowVM{
		Row: row.Row, StatusLabel: importStatusLabel(t, row.Status, dry), StatusClass: row.Status,
		Model: row.Model, Size: row.Size, Color: row.Color, SKU: row.SKU, Message: row.Message,
	}
}

func importBadRowLine(t tr, row catalog.ImportRowResult) string {
	line := t.T("admin.import.row") + " " + strconv.Itoa(row.Row)
	if row.Model != "" {
		line += " (" + row.Model + ")"
	}
	msg := row.Message
	if msg == "" {
		msg = importStatusLabel(t, row.Status, true)
	}
	return line + " — " + msg
}

// importStatusLabel is a row status in t's language.
func importStatusLabel(t tr, status string, dry bool) string {
	labels := map[string][2]string{
		catalog.RowStatusCreated: {"admin.import.st_created", "admin.import.st_will_create"},
		catalog.RowStatusUpdated: {"admin.import.st_updated", "admin.import.st_will_update"},
		catalog.RowStatusSkipped: {"admin.import.st_skipped", "admin.import.st_will_skip"},
		catalog.RowStatusError:   {"admin.import.st_error", "admin.import.st_error"},
	}
	pair, ok := labels[status]
	if !ok {
		return status
	}
	if dry {
		return t.T(pair[1])
	}
	return t.T(pair[0])
}
