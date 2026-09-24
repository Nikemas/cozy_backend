package catalog

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// Canonical import column names. Every accepted header (English key or
// the Russian caption used by the downloadable template) is mapped onto
// one of these by columnAliases.
const (
	colModelCode     = "model_code"
	colNameRu        = "name_ru"
	colNameKy        = "name_ky"
	colCategory      = "category"
	colBrand         = "brand"
	colPrice         = "price"
	colPriceOverride = "price_override"
	colDescriptionRu = "description_ru"
	colDescriptionKy = "description_ky"
	colSize          = "size"
	colColor         = "color"
	colSKU           = "sku"
	colQuantity      = "quantity"
)

// stockPointColumnPrefix marks an optional per-point stock column, e.g.
// "stock:11111111-1111-1111-1111-111111111111". The plain "quantity"
// column (Остаток) is the usual way to set stock — it goes to the point
// chosen in the import form — these columns remain for files that carry
// stock for several points at once.
const stockPointColumnPrefix = "stock:"

// columnAliases maps a normalized header (see normalizeHeader) to its
// canonical column. The Russian captions are what the template
// (BuildImportTemplate) writes, so a customer's own price list only needs
// its header row renamed to match.
var columnAliases = map[string]string{
	"model_code": colModelCode, "model": colModelCode, "article": colModelCode,
	"артикул": colModelCode, "артикул модели": colModelCode, "модель": colModelCode,

	"name_ru": colNameRu, "name": colNameRu, "название": colNameRu,
	"наименование": colNameRu, "название (ru)": colNameRu,

	"name_ky": colNameKy, "название (ky)": colNameKy, "название (кырг.)": colNameKy, "аталышы": colNameKy,

	"category": colCategory, "категория": colCategory,

	"brand": colBrand, "бренд": colBrand,

	"price": colPrice, "цена": colPrice,

	"price_override": colPriceOverride, "цена варианта": colPriceOverride,

	"description_ru": colDescriptionRu, "description": colDescriptionRu, "описание": colDescriptionRu,
	"описание (ru)": colDescriptionRu,

	"description_ky": colDescriptionKy, "описание (ky)": colDescriptionKy,

	"size": colSize, "размер": colSize,

	"color": colColor, "colour": colColor, "цвет": colColor,

	"sku": colSKU, "штрихкод": colSKU, "barcode": colSKU, "код варианта": colSKU,

	"quantity": colQuantity, "qty": colQuantity, "stock": colQuantity,
	"остаток": colQuantity, "количество": colQuantity, "кол-во": colQuantity,
}

// requiredColumns must be present in the header row; without them no row
// could ever be imported, so the file is rejected up front.
var requiredColumns = []struct{ key, caption string }{
	{colNameRu, "Название (name_ru)"},
	{colCategory, "Категория (category)"},
	{colPrice, "Цена (price)"},
}

// importRow is the common intermediate shape both CSV and XLSX parsing
// produce: canonical-column -> trimmed cell value, plus the spreadsheet row
// number (the header is row 1) for the report.
type importRow struct {
	line   int
	fields map[string]string
}

func (row importRow) get(col string) string { return row.fields[col] }

// isBlank reports whether every cell in the row is empty — such rows are
// skipped silently rather than reported.
func (row importRow) isBlank() bool {
	for _, v := range row.fields {
		if v != "" {
			return false
		}
	}
	return true
}

var spaceRunRE = regexp.MustCompile(`\s+`)

// normalizeHeader lowercases, trims and collapses whitespace in a header
// cell, strips a UTF-8 BOM (Excel CSV exports) and a trailing "*" (the
// template marks required columns that way).
func normalizeHeader(h string) string {
	h = strings.TrimPrefix(h, "\ufeff")
	h = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(h), "*"))
	return strings.ToLower(spaceRunRE.ReplaceAllString(h, " "))
}

// canonicalHeaders maps raw header cells to canonical columns. Unknown
// headers map to "" (ignored). A column given twice is a file-level error:
// silently picking one would import the wrong data.
func canonicalHeaders(raw []string) ([]string, error) {
	out := make([]string, len(raw))
	seen := map[string]string{}
	for i, h := range raw {
		n := normalizeHeader(h)
		if n == "" {
			continue
		}
		var col string
		if strings.HasPrefix(n, stockPointColumnPrefix) {
			col = stockPointColumnPrefix + strings.TrimSpace(strings.TrimPrefix(n, stockPointColumnPrefix))
		} else if c, ok := columnAliases[n]; ok {
			col = c
		} else {
			continue
		}
		if prev, dup := seen[col]; dup {
			return nil, apperr.BadRequest("duplicate_column",
				fmt.Sprintf("колонка %q повторяется (%q и %q)", col, prev, strings.TrimSpace(h)))
		}
		seen[col] = strings.TrimSpace(h)
		out[i] = col
	}
	var missing []string
	for _, rc := range requiredColumns {
		if _, ok := seen[rc.key]; !ok {
			missing = append(missing, rc.caption)
		}
	}
	if len(missing) > 0 {
		return nil, apperr.BadRequest("missing_columns",
			"в файле нет обязательных колонок: "+strings.Join(missing, ", ")+" — скачайте шаблон импорта")
	}
	return out, nil
}

// buildRow zips headers with record into an importRow, tolerating a record
// shorter than headers (missing trailing columns count as empty).
func buildRow(headers []string, record []string, line int) importRow {
	fields := make(map[string]string, len(headers))
	for i, h := range headers {
		if h == "" {
			continue
		}
		v := ""
		if i < len(record) {
			v = strings.TrimSpace(strings.ReplaceAll(record[i], "\u00a0", " "))
		}
		fields[h] = v
	}
	return importRow{line: line, fields: fields}
}

// parseCSVRows reads r as CSV: the first record is the header row (line 1).
// The delimiter is sniffed from the header line: Excel with a Russian
// locale exports ";"-separated "CSV".
func parseCSVRows(r io.Reader) ([]importRow, error) {
	br := bufio.NewReader(r)
	first, err := br.Peek(4096)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, apperr.BadRequest("invalid_csv", "не удалось прочитать CSV")
	}
	if i := bytes.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}

	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1 // tolerate rows with fewer trailing columns than the header
	if bytes.Count(first, []byte(";")) > bytes.Count(first, []byte(",")) {
		cr.Comma = ';'
	}

	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, apperr.BadRequest("empty_file", "файл импорта пустой")
	}
	if err != nil {
		return nil, apperr.BadRequest("invalid_csv", "не удалось прочитать CSV: "+err.Error())
	}
	headers, err := canonicalHeaders(header)
	if err != nil {
		return nil, err
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

// xlsxUnzipSizeLimit / xlsxUnzipXMLSizeLimit cap how much an uploaded
// .xlsx (a zip) may decompress to — in total, and per worksheet XML kept
// in memory. excelize's defaults (16 GiB total) would let a few-MB "zip
// bomb" exhaust memory/disk; a real product import is far below 64 MiB
// unpacked. Variables so tests can lower them.
var (
	xlsxUnzipSizeLimit    int64 = 64 << 20
	xlsxUnzipXMLSizeLimit int64 = 16 << 20
)

// parseXLSXRows reads r as an .xlsx workbook, using its first sheet: the
// first row is the header row (line 1). Cells are read raw (unformatted),
// so a price shown as "4 999,00 сом" by a number format still arrives as
// "4999".
func parseXLSXRows(r io.Reader) ([]importRow, error) {
	f, err := excelize.OpenReader(r, excelize.Options{
		UnzipSizeLimit:    xlsxUnzipSizeLimit,
		UnzipXMLSizeLimit: xlsxUnzipXMLSizeLimit,
	})
	if err != nil {
		return nil, apperr.BadRequest("invalid_xlsx", "не удалось прочитать Excel-файл: "+err.Error())
	}
	defer func() { _ = f.Close() }()

	sheet := f.GetSheetName(0)
	if sheet == "" {
		return nil, apperr.BadRequest("empty_file", "в Excel-файле нет листов")
	}
	allRows, err := f.GetRows(sheet, excelize.Options{RawCellValue: true})
	if err != nil {
		return nil, apperr.BadRequest("invalid_xlsx", "не удалось прочитать лист: "+err.Error())
	}
	if len(allRows) == 0 {
		return nil, apperr.BadRequest("empty_file", "файл импорта пустой")
	}

	headers, err := canonicalHeaders(allRows[0])
	if err != nil {
		return nil, err
	}

	rows := make([]importRow, 0, len(allRows)-1)
	for i, record := range allRows[1:] {
		rows = append(rows, buildRow(headers, record, i+2)) // +2: header is line 1, allRows[1] is line 2
	}
	return rows, nil
}

// --- cell value normalization ---

// maxImportPrice is the largest value NUMERIC(10,2) holds.
const maxImportPrice = 99_999_999.99

// maxImportQuantity is a sanity cap on one stock cell: far above any real
// shoe store's stock, low enough to catch a price pasted into Остаток.
const maxImportQuantity = 1_000_000

var (
	decimalRE = regexp.MustCompile(`^\d+(\.\d+)?$`)
	integerRE = regexp.MustCompile(`^\d+(\.0+)?$`)
)

// compactNumber strips the grouping spaces and currency suffix people type
// into price cells ("4 999,50 сом") and turns a decimal comma into a dot.
func compactNumber(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, "сом")
	s = strings.TrimSuffix(s, "kgs")
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\u00a0' || r == '\u202f' || r == '\t' {
			return -1
		}
		return r
	}, s)
	return strings.ReplaceAll(s, ",", ".")
}

// parsePrice parses a money cell. Only plain non-negative decimals are
// accepted — strconv.ParseFloat alone would also take "NaN", "Inf", hex
// floats and exponents, none of which belong in a price list.
func parsePrice(raw string) (float64, error) {
	s := compactNumber(raw)
	if strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("цена не может быть отрицательной: %q", raw)
	}
	if !decimalRE.MatchString(s) {
		return 0, fmt.Errorf("цена не число: %q", raw)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("цена не число: %q", raw)
	}
	f = math.Round(f*100) / 100
	if f > maxImportPrice {
		return 0, fmt.Errorf("слишком большая цена: %q", raw)
	}
	return f, nil
}

// parseQuantity parses a stock cell: a non-negative whole number ("5" or
// Excel's "5.0").
func parseQuantity(raw string) (int, error) {
	s := compactNumber(raw)
	if strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("остаток не может быть отрицательным: %q", raw)
	}
	if !integerRE.MatchString(s) {
		return 0, fmt.Errorf("остаток должен быть целым числом: %q", raw)
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.Atoi(s)
	if err != nil || n > maxImportQuantity {
		return 0, fmt.Errorf("слишком большой остаток: %q", raw)
	}
	return n, nil
}

// normalizeText trims and collapses internal whitespace runs.
func normalizeText(s string) string {
	return strings.TrimSpace(spaceRunRE.ReplaceAllString(s, " "))
}

// normalizeSize trims a size cell, collapses whitespace and turns a
// decimal comma into a dot ("42,5" -> "42.5") so the same size typed two
// ways maps to one variant.
func normalizeSize(s string) string {
	return strings.ReplaceAll(normalizeText(s), ",", ".")
}
