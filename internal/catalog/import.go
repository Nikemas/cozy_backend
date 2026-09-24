package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// ImportFormat identifies the shape of a bulk product import file, per §8
// of the ТЗ (admin panel: "массовая загрузка товаров — импорт CSV/Excel").
type ImportFormat int

const (
	ImportFormatCSV ImportFormat = iota
	ImportFormatXLSX
)

// Row statuses in ImportResult.Rows. In a dry run "created"/"updated" mean
// "would be created/updated".
const (
	RowStatusCreated = "created"
	RowStatusUpdated = "updated"
	RowStatusSkipped = "skipped" // valid row of a model that failed elsewhere
	RowStatusError   = "error"
)

// RowError is the legacy per-row error shape (kept in ImportResult.Errors
// for existing API clients). Row is 1-based and counts the header as row 1.
type RowError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

// ImportRowResult reports what happened to one non-blank data row.
type ImportRowResult struct {
	Row     int    `json:"row"`
	Status  string `json:"status"`
	Model   string `json:"model,omitempty"` // model article, or product name when there is none
	Size    string `json:"size,omitempty"`
	Color   string `json:"color,omitempty"`
	SKU     string `json:"sku,omitempty"`
	Message string `json:"message,omitempty"`
}

// ImportSummary counts rows by status and products/variants by outcome.
type ImportSummary struct {
	Rows            int `json:"rows"`
	Created         int `json:"created"`
	Updated         int `json:"updated"`
	Skipped         int `json:"skipped"`
	Errors          int `json:"errors"`
	ProductsCreated int `json:"products_created"`
	ProductsUpdated int `json:"products_updated"`
	VariantsCreated int `json:"variants_created"`
	VariantsUpdated int `json:"variants_updated"`
}

// ImportResult is the outcome of ImportProducts.
type ImportResult struct {
	DryRun bool `json:"dry_run"`
	// PointID is where the "Остаток" (quantity) column went; nil when the
	// file had no quantities or there is no active point.
	PointID *string           `json:"point_id"`
	Summary ImportSummary     `json:"summary"`
	Rows    []ImportRowResult `json:"rows"`

	// Imported (products created + updated) and Errors (error and skipped
	// rows) keep the pre-2026-09 response shape working.
	Imported int        `json:"imported"`
	Errors   []RowError `json:"errors"`
}

// ImportOptions tune one ImportProducts run.
type ImportOptions struct {
	// DryRun validates everything — including against the database, inside
	// a transaction that is always rolled back — and reports what would
	// happen without writing anything.
	DryRun bool
	// PointID is the point of sale the quantity column is stored at. Empty
	// means the first active point (by creation time).
	PointID string
}

// ImportProduct is the product-level data of one model.
type ImportProduct struct {
	ModelCode     string
	CategoryID    string
	NameRu        string
	NameKy        string
	Brand         string
	DescriptionRu string
	DescriptionKy string
	BasePrice     float64
}

// ImportVariant is one size × color row.
type ImportVariant struct {
	Size          string
	Color         string
	SKU           string
	PriceOverride *float64
}

// ImportVariantRef identifies an existing variant.
type ImportVariantRef struct {
	ID        string
	ProductID string
	// ProductModelCode is the owning product's article ("" if none).
	ProductModelCode string
}

// ImportStore is the database side of the importer. Lookups that don't
// need a transaction live here; writes go through a session so each model
// is all-or-nothing. NewSQLImportStore is the Postgres implementation.
type ImportStore interface {
	// ResolveCategory maps a category cell (UUID, slug or RU/KY name) to
	// its id; the error text is shown to the admin as-is.
	ResolveCategory(ctx context.Context, raw string) (string, error)
	// ActivePoint reports whether id is an active point of sale.
	ActivePoint(ctx context.Context, id string) (bool, error)
	// DefaultPoint returns the first active point of sale, "" if none.
	DefaultPoint(ctx context.Context) (string, error)
	// Begin opens a session; with dryRun nothing it does is ever committed.
	Begin(ctx context.Context, dryRun bool) (ImportSession, error)
}

// ImportSession runs models atomically.
type ImportSession interface {
	// Model runs fn in its own transaction (or savepoint, in a dry run):
	// if fn fails nothing it did is kept.
	Model(ctx context.Context, fn func(tx ImportTx) error) error
	Close() error
}

// ImportTx is what one model's import may do. Lookups return ""/nil when
// nothing matches.
type ImportTx interface {
	ProductByModelCode(ctx context.Context, code string) (string, error)
	// ProductsByName finds products by brand + name + category; when
	// modelCode is non-empty only products without a model code qualify
	// (a product with a different code is a different model).
	ProductsByName(ctx context.Context, brand, nameRu, categoryID, modelCode string) ([]string, error)
	CreateProduct(ctx context.Context, p ImportProduct) (string, error)
	UpdateProduct(ctx context.Context, id string, p ImportProduct) error

	VariantBySKU(ctx context.Context, sku string) (*ImportVariantRef, error)
	VariantBySizeColor(ctx context.Context, productID, size, color string) (*ImportVariantRef, error)
	CreateVariant(ctx context.Context, productID string, v ImportVariant) (string, error)
	UpdateVariant(ctx context.Context, id string, v ImportVariant) error

	SetStock(ctx context.Context, variantID, pointID string, qty int) error
}

// DetectImportFormat decides whether an uploaded import file is CSV or
// XLSX from its filename extension and/or declared Content-Type. The
// extension wins when present and recognized; Content-Type is the
// fallback. ok=false means "reject before parsing".
func DetectImportFormat(filename, contentType string) (format ImportFormat, ok bool) {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".csv":
		return ImportFormatCSV, true
	case ".xlsx":
		return ImportFormatXLSX, true
	}

	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.Index(ct, ";"); i >= 0 { // strip "; charset=utf-8" etc.
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "text/csv", "application/csv":
		return ImportFormatCSV, true
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ImportFormatXLSX, true
	}

	return 0, false
}

// ImportProducts parses r (CSV or XLSX), groups its rows into models (one
// product per model: by the article column when present, otherwise by
// brand + name + category; each row is a size × color variant), validates
// everything, and then imports model by model, each in one transaction.
//
// Re-importing the same file updates instead of duplicating: a product is
// matched by article, then by any of its rows' SKUs, then by brand + name
// + category; a variant by SKU, then by size + color.
//
// A model with any invalid row is not imported at all (its other rows are
// reported as skipped); other models are unaffected. A non-nil error
// return means the file (or the request) as a whole is unusable.
func ImportProducts(ctx context.Context, r io.Reader, format ImportFormat, store ImportStore, opts ImportOptions) (*ImportResult, error) {
	var rows []importRow
	var err error
	switch format {
	case ImportFormatCSV:
		rows, err = parseCSVRows(r)
	case ImportFormatXLSX:
		rows, err = parseXLSXRows(r)
	default:
		return nil, apperr.BadRequest("unsupported_format", "неподдерживаемый формат файла импорта")
	}
	if err != nil {
		return nil, err
	}

	pointID, err := resolveTargetPoint(ctx, store, opts.PointID)
	if err != nil {
		return nil, err
	}

	p := &planner{ctx: ctx, store: store, pointID: pointID,
		categories: map[string]categoryLookup{}, points: map[string]bool{}}
	models, err := p.plan(rows)
	if err != nil {
		return nil, err
	}

	res := &ImportResult{DryRun: opts.DryRun, Rows: []ImportRowResult{}, Errors: []RowError{}}
	if p.usedPoint {
		res.PointID = &pointID
	}
	if len(models) == 0 {
		return res, nil
	}

	sess, err := store.Begin(ctx, opts.DryRun)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()

	for _, m := range models {
		if m.failed() {
			res.addModel(m, nil)
			continue
		}
		var out modelOutcome
		err := sess.Model(ctx, func(tx ImportTx) error {
			out = modelOutcome{}
			return importModel(ctx, tx, m, &out)
		})
		if err != nil {
			var rf *rowFailure
			if !errors.As(err, &rf) {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				rf = &rowFailure{line: m.rows[0].line, msg: dbErrorMessage(err)}
			}
			m.fail(rf.line, rf.msg)
			res.addModel(m, nil)
			continue
		}
		res.addModel(m, &out)
	}
	sort.SliceStable(res.Rows, func(i, j int) bool { return res.Rows[i].Row < res.Rows[j].Row })
	sort.SliceStable(res.Errors, func(i, j int) bool { return res.Errors[i].Row < res.Errors[j].Row })
	return res, nil
}

func resolveTargetPoint(ctx context.Context, store ImportStore, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return store.DefaultPoint(ctx)
	}
	if _, err := uuid.Parse(requested); err != nil {
		return "", apperr.BadRequest("invalid_point", "точка продаж не найдена")
	}
	ok, err := store.ActivePoint(ctx, requested)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", apperr.BadRequest("invalid_point", "точка продаж не найдена или отключена")
	}
	return requested, nil
}

// rowFailure is a failure attributed to one spreadsheet row.
type rowFailure struct {
	line int
	msg  string
}

func (f *rowFailure) Error() string { return fmt.Sprintf("строка %d: %s", f.line, f.msg) }

// modelOutcome is what importModel did, for the report.
type modelOutcome struct {
	productCreated bool
	rowStatus      map[int]string // line -> created/updated
	variantsNew    int
	variantsUpd    int
}

// importModel writes one validated model through tx.
func importModel(ctx context.Context, tx ImportTx, m *plannedModel, out *modelOutcome) error {
	out.rowStatus = map[int]string{}
	cur := m.rows[0].line
	wrap := func(err error) error {
		var rf *rowFailure
		if errors.As(err, &rf) {
			return err
		}
		return &rowFailure{line: cur, msg: dbErrorMessage(err)}
	}

	productID, err := findProduct(ctx, tx, m)
	if err != nil {
		return wrap(err)
	}
	if productID == "" {
		if productID, err = tx.CreateProduct(ctx, m.product); err != nil {
			return wrap(err)
		}
		out.productCreated = true
	} else if err := tx.UpdateProduct(ctx, productID, m.product); err != nil {
		return wrap(err)
	}

	for _, row := range m.rows {
		cur = row.line
		if !row.hasVariant {
			out.rowStatus[row.line] = productStatus(out.productCreated)
			continue
		}
		var ref *ImportVariantRef
		if row.variant.SKU != "" {
			if ref, err = tx.VariantBySKU(ctx, row.variant.SKU); err != nil {
				return wrap(err)
			}
			if ref != nil && ref.ProductID != productID {
				return &rowFailure{line: row.line, msg: fmt.Sprintf("SKU %q уже используется у другого товара", row.variant.SKU)}
			}
		}
		if ref == nil && !out.productCreated {
			if ref, err = tx.VariantBySizeColor(ctx, productID, row.variant.Size, row.variant.Color); err != nil {
				return wrap(err)
			}
		}
		var variantID string
		if ref == nil {
			if variantID, err = tx.CreateVariant(ctx, productID, row.variant); err != nil {
				return wrap(err)
			}
			out.rowStatus[row.line] = RowStatusCreated
			out.variantsNew++
		} else {
			variantID = ref.ID
			if err := tx.UpdateVariant(ctx, variantID, row.variant); err != nil {
				return wrap(err)
			}
			out.rowStatus[row.line] = RowStatusUpdated
			out.variantsUpd++
		}
		for _, s := range row.stock {
			if err := tx.SetStock(ctx, variantID, s.pointID, s.qty); err != nil {
				return wrap(err)
			}
		}
	}
	return nil
}

func productStatus(created bool) string {
	if created {
		return RowStatusCreated
	}
	return RowStatusUpdated
}

// findProduct matches an existing product for m: by article, then by the
// SKUs of its rows, then by brand + name + category. "" means "create".
func findProduct(ctx context.Context, tx ImportTx, m *plannedModel) (string, error) {
	if m.product.ModelCode != "" {
		id, err := tx.ProductByModelCode(ctx, m.product.ModelCode)
		if err != nil || id != "" {
			return id, err
		}
	}

	var bySKU string
	for _, row := range m.rows {
		if !row.hasVariant || row.variant.SKU == "" {
			continue
		}
		ref, err := tx.VariantBySKU(ctx, row.variant.SKU)
		if err != nil {
			return "", err
		}
		if ref == nil {
			continue
		}
		if m.product.ModelCode != "" && ref.ProductModelCode != "" && !strings.EqualFold(ref.ProductModelCode, m.product.ModelCode) {
			return "", &rowFailure{line: row.line, msg: fmt.Sprintf("SKU %q уже используется у другого товара (артикул %s)", row.variant.SKU, ref.ProductModelCode)}
		}
		if bySKU != "" && ref.ProductID != bySKU {
			return "", &rowFailure{line: row.line, msg: fmt.Sprintf("SKU %q относится к другому товару, чем остальные строки модели", row.variant.SKU)}
		}
		bySKU = ref.ProductID
	}
	if bySKU != "" {
		return bySKU, nil
	}

	ids, err := tx.ProductsByName(ctx, m.product.Brand, m.product.NameRu, m.product.CategoryID, m.product.ModelCode)
	if err != nil {
		return "", err
	}
	switch len(ids) {
	case 0:
		return "", nil
	case 1:
		return ids[0], nil
	default:
		return "", &rowFailure{line: m.rows[0].line,
			msg: fmt.Sprintf("найдено несколько товаров «%s» в этой категории — укажите артикул модели или SKU", m.product.NameRu)}
	}
}

// dbErrorMessage turns a database error into admin-facing text. Constraint
// violations are the expected cases (a size/color or SKU clash, a stale
// point id); anything else is logged and reported generically.
func dbErrorMessage(err error) string {
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		return ae.Message
	}
	switch pgErrCode(err) {
	case pgUniqueViolation:
		return "конфликт с существующими данными: такой размер/цвет, SKU или артикул уже есть у другого товара/вариации"
	case pgForeignKeyViolation:
		return "категория или точка продаж не найдена"
	case pgCheckViolation:
		return "значение вне допустимого диапазона"
	}
	slog.Error("catalog import: database error", "err", err)
	return "внутренняя ошибка при сохранении — попробуйте ещё раз"
}

// addModel records m's rows in the report. out is nil when the model was
// not imported (its failing rows are errors, the rest skipped).
func (res *ImportResult) addModel(m *plannedModel, out *modelOutcome) {
	if out != nil {
		if out.productCreated {
			res.Summary.ProductsCreated++
		} else {
			res.Summary.ProductsUpdated++
		}
		res.Imported++
		res.Summary.VariantsCreated += out.variantsNew
		res.Summary.VariantsUpdated += out.variantsUpd
	}
	for _, row := range m.rows {
		r := ImportRowResult{Row: row.line, Model: m.label}
		if row.hasVariant {
			r.Size, r.Color, r.SKU = row.variant.Size, row.variant.Color, row.variant.SKU
		}
		switch {
		case out != nil:
			r.Status = out.rowStatus[row.line]
		case row.err != "":
			r.Status, r.Message = RowStatusError, row.err
		default:
			r.Status = RowStatusSkipped
			r.Message = fmt.Sprintf("модель не импортирована из-за ошибки в строке %d", m.firstErrLine())
		}
		res.Summary.Rows++
		switch r.Status {
		case RowStatusCreated:
			res.Summary.Created++
		case RowStatusUpdated:
			res.Summary.Updated++
		case RowStatusSkipped:
			res.Summary.Skipped++
			res.Errors = append(res.Errors, RowError{Row: r.Row, Message: r.Message})
		case RowStatusError:
			res.Summary.Errors++
			res.Errors = append(res.Errors, RowError{Row: r.Row, Message: r.Message})
		}
		res.Rows = append(res.Rows, r)
	}
}
