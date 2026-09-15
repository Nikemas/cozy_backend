package catalog

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// ImportFormat identifies the shape of a bulk product import file, per §8
// of the ТЗ (admin panel: "массовая загрузка товаров — импорт CSV/Excel").
type ImportFormat int

const (
	ImportFormatCSV ImportFormat = iota
	ImportFormatXLSX
)

// stockPointColumnPrefix marks an optional per-point stock column, e.g.
// "stock:11111111-1111-1111-1111-111111111111" — the header suffix after
// the colon is passed straight through to StockRepo.Upsert as pointID.
// Unlike category, there is no id-or-slug resolver for points of sale in
// this codebase yet (Task G, building the points-of-sale admin API, is a
// separate concurrent stream), so this deliberately only accepts the raw
// point UUID rather than a human-friendly slug.
const stockPointColumnPrefix = "stock:"

// RowError describes one row of an import batch that failed — either a
// validation error, or a failure from ProductRepo/VariantRepo/StockRepo.
// Row is 1-based and counts the header as row 1, so it points at the same
// row number a spreadsheet application would show (the first data row is
// row 2) — this is what an admin fixing the file needs, not an offset into
// only the data rows.
type RowError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

// ImportResult is the outcome of ImportProducts.
type ImportResult struct {
	Imported int        `json:"imported"`
	Errors   []RowError `json:"errors"`
}

// productCreator is the subset of *ProductRepo the importer depends on, so
// tests can inject a fake instead of a live database — mirrors the
// stockUpserter pattern in internal/httpapi/admin_catalog.go.
type productCreator interface {
	Create(ctx context.Context, in ProductInput) (*Product, error)
}

// variantCreator is the subset of *VariantRepo the importer depends on.
type variantCreator interface {
	Create(ctx context.Context, productID string, in VariantInput) (*Variant, error)
}

// categoryResolver is the subset of *CategoryRepo the importer depends on.
type categoryResolver interface {
	ResolveID(ctx context.Context, idOrSlug string) (string, error)
}

// stockSetter is the subset of *StockRepo the importer depends on for the
// optional per-point stock columns.
type stockSetter interface {
	Upsert(ctx context.Context, variantID, pointID string, quantity int) (*StockEntry, error)
}

// ImportDeps bundles everything ImportProducts needs to create rows,
// behind small interfaces so it's fully testable without a live database
// (construct it directly with fakes) or with the real repos (the fields
// accept *ProductRepo/*VariantRepo/*CategoryRepo/*StockRepo as-is).
type ImportDeps struct {
	Categories categoryResolver
	Products   productCreator
	Variants   variantCreator
	// Stock is optional: a nil Stock disables per-point stock columns
	// entirely (they're skipped rather than erroring), since §8 of the ТЗ
	// only asks for "опционально размеры/цвета/остатки по точкам" — stock
	// is a nice-to-have on top of product+variant creation, not a hard
	// requirement.
	Stock stockSetter
}

// importRow is the common intermediate shape both CSV and XLSX parsing
// produce: a header-name -> trimmed-cell-value map, plus the originating
// row number for error reporting.
type importRow struct {
	line   int
	fields map[string]string
}

func (row importRow) get(header string) string {
	return row.fields[header]
}

// isBlank reports whether every cell in the row is empty — such rows (a
// stray blank line at the end of a CSV export, or a fully empty Excel row)
// are skipped silently rather than reported as errors.
func (row importRow) isBlank() bool {
	for _, v := range row.fields {
		if v != "" {
			return false
		}
	}
	return true
}

// DetectImportFormat decides whether an uploaded import file is CSV or
// XLSX from its filename extension and/or declared Content-Type. The
// extension wins when present and recognized; Content-Type is checked as a
// fallback so a browser sending a generic type for a correctly-named file
// doesn't get rejected, and also so a file whose name has no extension at
// all can still be recognized. An unrecognized combination reports ok=false
// so the HTTP handler can reject the upload with apperr.BadRequest before
// attempting to parse anything.
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

// ImportProducts parses r as either CSV or XLSX (per format) and, for each
// data row, resolves its category, validates required fields, and creates
// the product — plus a variant when size/color are both present, plus
// per-point stock rows when deps.Stock is set and the row has any
// "stock:<pointId>" columns. A row that fails validation or creation is
// recorded in the result's Errors and the batch continues with the next
// row: one bad row must never abort the rest of the import.
//
// A non-nil error return (as opposed to a populated ImportResult.Errors)
// means the file itself couldn't be read at all — e.g. malformed CSV
// syntax or an unreadable Excel file — so nothing could be imported.
func ImportProducts(ctx context.Context, r io.Reader, format ImportFormat, deps ImportDeps) (*ImportResult, error) {
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

	result := &ImportResult{Errors: []RowError{}}
	for _, row := range rows {
		if row.isBlank() {
			continue
		}

		warnings, err := importRowOne(ctx, deps, row)
		if err != nil {
			result.Errors = append(result.Errors, RowError{Row: row.line, Message: err.Error()})
			continue
		}
		result.Imported++
		for _, w := range warnings {
			result.Errors = append(result.Errors, RowError{Row: row.line, Message: w})
		}
	}
	return result, nil
}

// importRowOne creates the product (and optional variant/stock) for a
// single row. The returned error, if any, means the row as a whole failed
// (nothing importable happened) and is recorded as-is — apperr messages
// are already human-readable Russian text. The returned warnings are
// non-fatal notes about optional stock columns that failed after the
// product itself was already created successfully; the row still counts as
// imported when only warnings (no error) come back.
func importRowOne(ctx context.Context, deps ImportDeps, row importRow) ([]string, error) {
	nameRu := row.get("name_ru")
	nameKy := row.get("name_ky")
	categoryRaw := row.get("category")
	priceRaw := row.get("price")

	if nameRu == "" {
		return nil, errors.New("name_ru обязателен")
	}
	if nameKy == "" {
		return nil, errors.New("name_ky обязателен")
	}
	if categoryRaw == "" {
		return nil, errors.New("category обязателен")
	}
	if priceRaw == "" {
		return nil, errors.New("price обязателен")
	}

	price, err := strconv.ParseFloat(priceRaw, 64)
	if err != nil {
		return nil, fmt.Errorf("некорректная цена %q", priceRaw)
	}
	if price < 0 {
		return nil, errors.New("price не может быть отрицательным")
	}

	categoryID, err := deps.Categories.ResolveID(ctx, categoryRaw)
	if err != nil {
		return nil, fmt.Errorf("категория %q не найдена", categoryRaw)
	}

	product, err := deps.Products.Create(ctx, ProductInput{
		CategoryID:    categoryID,
		NameRu:        nameRu,
		NameKy:        nameKy,
		DescriptionRu: optionalString(row.get("description_ru")),
		DescriptionKy: optionalString(row.get("description_ky")),
		Brand:         optionalString(row.get("brand")),
		BasePrice:     price,
		IsActive:      true,
	})
	if err != nil {
		return nil, err
	}

	size := row.get("size")
	color := row.get("color")
	var variant *Variant
	switch {
	case size != "" && color != "":
		priceOverride, err := optionalFloat(row.get("price_override"))
		if err != nil {
			return nil, fmt.Errorf("некорректный price_override %q", row.get("price_override"))
		}
		variant, err = deps.Variants.Create(ctx, product.ID, VariantInput{
			Size:          size,
			Color:         color,
			SKU:           optionalString(row.get("sku")),
			PriceOverride: priceOverride,
		})
		if err != nil {
			return nil, err
		}
	case size != "" || color != "":
		return nil, errors.New("для вариации нужны оба поля: size и color")
	}

	if deps.Stock == nil || variant == nil {
		return nil, nil
	}

	var warnings []string
	for header, value := range row.fields {
		pointID, ok := strings.CutPrefix(header, stockPointColumnPrefix)
		if !ok || value == "" {
			continue
		}
		qty, err := strconv.Atoi(value)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("товар создан, но остаток по точке %s не выставлен: некорректное количество %q", pointID, value))
			continue
		}
		if _, err := deps.Stock.Upsert(ctx, variant.ID, pointID, qty); err != nil {
			warnings = append(warnings, fmt.Sprintf("товар создан, но остаток по точке %s не выставлен: %s", pointID, err.Error()))
		}
	}
	return warnings, nil
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optionalFloat(s string) (*float64, error) {
	if s == "" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// normalizeHeader lowercases and trims a header cell, and strips a leading
// UTF-8 byte-order mark if present — common in CSV files exported from
// Excel, which would otherwise corrupt the first column's name (it would no
// longer match a plain "name_ru").
func normalizeHeader(h string) string {
	h = strings.TrimPrefix(h, "\ufeff")
	return strings.ToLower(strings.TrimSpace(h))
}

// buildRow zips headers with record into an importRow, tolerating a record
// shorter than headers (missing trailing columns count as empty) —
// encoding/csv is configured to allow this too (see parseCSVRows).
func buildRow(headers []string, record []string, line int) importRow {
	fields := make(map[string]string, len(headers))
	for i, h := range headers {
		if h == "" {
			continue
		}
		if i < len(record) {
			fields[h] = strings.TrimSpace(record[i])
		} else {
			fields[h] = ""
		}
	}
	return importRow{line: line, fields: fields}
}

// parseCSVRows reads r as CSV: the first record is the header row (line 1),
// every subsequent record a data row.
func parseCSVRows(r io.Reader) ([]importRow, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // tolerate rows with fewer trailing columns than the header

	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, apperr.BadRequest("empty_file", "файл импорта пустой")
	}
	if err != nil {
		return nil, apperr.BadRequest("invalid_csv", "не удалось прочитать CSV: "+err.Error())
	}
	headers := make([]string, len(header))
	for i, h := range header {
		headers[i] = normalizeHeader(h)
	}

	var rows []importRow
	line := 1
	for {
		record, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, apperr.BadRequest("invalid_csv", "не удалось прочитать CSV: "+err.Error())
		}
		line++
		rows = append(rows, buildRow(headers, record, line))
	}
	return rows, nil
}

// parseXLSXRows reads r as an .xlsx workbook, using its first sheet: the
// first row is the header row (line 1), every subsequent row a data row.
func parseXLSXRows(r io.Reader) ([]importRow, error) {
	f, err := excelize.OpenReader(r)
	if err != nil {
		return nil, apperr.BadRequest("invalid_xlsx", "не удалось прочитать Excel-файл: "+err.Error())
	}
	defer func() { _ = f.Close() }()

	sheet := f.GetSheetName(0)
	if sheet == "" {
		return nil, apperr.BadRequest("empty_file", "в Excel-файле нет листов")
	}
	allRows, err := f.GetRows(sheet)
	if err != nil {
		return nil, apperr.BadRequest("invalid_xlsx", "не удалось прочитать лист: "+err.Error())
	}
	if len(allRows) == 0 {
		return nil, apperr.BadRequest("empty_file", "файл импорта пустой")
	}

	headers := make([]string, len(allRows[0]))
	for i, h := range allRows[0] {
		headers[i] = normalizeHeader(h)
	}

	rows := make([]importRow, 0, len(allRows)-1)
	for i, record := range allRows[1:] {
		rows = append(rows, buildRow(headers, record, i+2)) // +2: header is line 1, allRows[1] is line 2
	}
	return rows, nil
}
