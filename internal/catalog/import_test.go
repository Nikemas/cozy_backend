package catalog

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// --- fakes ---

type fakeCategoryResolver struct {
	// known maps an accepted idOrSlug to the UUID it resolves to.
	known map[string]string
}

func (f *fakeCategoryResolver) ResolveID(_ context.Context, idOrSlug string) (string, error) {
	if id, ok := f.known[idOrSlug]; ok {
		return id, nil
	}
	return "", apperr.NotFound("category_not_found", "категория не найдена")
}

type fakeProductCreator struct {
	created []ProductInput
	nextID  int
	failErr error // if set, every Create fails with this error
}

func (f *fakeProductCreator) Create(_ context.Context, in ProductInput) (*Product, error) {
	if f.failErr != nil {
		return nil, f.failErr
	}
	f.nextID++
	f.created = append(f.created, in)
	return &Product{ID: "product-" + itoa(f.nextID), CategoryID: in.CategoryID, NameRu: in.NameRu, NameKy: in.NameKy, BasePrice: in.BasePrice}, nil
}

type fakeVariantCreator struct {
	created []VariantInput
	nextID  int
	failErr error
}

func (f *fakeVariantCreator) Create(_ context.Context, productID string, in VariantInput) (*Variant, error) {
	if f.failErr != nil {
		return nil, f.failErr
	}
	f.nextID++
	f.created = append(f.created, in)
	return &Variant{ID: "variant-" + itoa(f.nextID), ProductID: productID, Size: in.Size, Color: in.Color}, nil
}

type fakeStockSetter struct {
	upserts []StockEntry
	failFor map[string]bool // pointID -> fail this upsert
}

func (f *fakeStockSetter) Upsert(_ context.Context, variantID, pointID string, quantity int) (*StockEntry, error) {
	if f.failFor[pointID] {
		return nil, apperr.BadRequest("invalid_variant_or_point", "вариация или точка продаж не найдена")
	}
	e := StockEntry{VariantID: variantID, PointID: pointID, Quantity: quantity}
	f.upserts = append(f.upserts, e)
	return &e, nil
}

func itoa(n int) string {
	digits := "0123456789"
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{digits[n%10]}, b...)
		n /= 10
	}
	return string(b)
}

func baseDeps() (ImportDeps, *fakeCategoryResolver, *fakeProductCreator, *fakeVariantCreator, *fakeStockSetter) {
	cats := &fakeCategoryResolver{known: map[string]string{
		"sneakers":                             "cat-sneakers",
		"11111111-1111-1111-1111-111111111111": "11111111-1111-1111-1111-111111111111",
	}}
	products := &fakeProductCreator{}
	variants := &fakeVariantCreator{}
	stock := &fakeStockSetter{failFor: map[string]bool{}}
	return ImportDeps{Categories: cats, Products: products, Variants: variants, Stock: stock}, cats, products, variants, stock
}

// --- CSV: valid rows ---

func TestImportProductsCSVAllValid(t *testing.T) {
	deps, _, products, variants, _ := baseDeps()

	csvData := "name_ru,name_ky,category,price\n" +
		"Кроссовки Nike,Найк кроссовкалар,sneakers,4999\n" +
		"Кроссовки Adidas,Адидас кроссовкалар,sneakers,3999.50\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 2 {
		t.Errorf("Imported = %d, want 2", result.Imported)
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors = %+v, want none", result.Errors)
	}
	if len(products.created) != 2 {
		t.Fatalf("expected 2 products created, got %d", len(products.created))
	}
	if products.created[0].CategoryID != "cat-sneakers" {
		t.Errorf("CategoryID = %q, want resolved cat-sneakers", products.created[0].CategoryID)
	}
	if len(variants.created) != 0 {
		t.Errorf("expected no variants created (no size/color columns), got %d", len(variants.created))
	}
}

// --- CSV: missing required field ---

func TestImportProductsCSVMissingRequiredField(t *testing.T) {
	deps, _, products, _, _ := baseDeps()

	csvData := "name_ru,name_ky,category,price\n" +
		"Кроссовки Nike,,sneakers,4999\n" + // missing name_ky
		"Кроссовки Adidas,Адидас кроссовкалар,sneakers,3999\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 1 {
		t.Errorf("Imported = %d, want 1", result.Imported)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %+v, want exactly 1", result.Errors)
	}
	if result.Errors[0].Row != 2 {
		t.Errorf("Row = %d, want 2 (header is row 1)", result.Errors[0].Row)
	}
	if !strings.Contains(result.Errors[0].Message, "name_ky") {
		t.Errorf("Message = %q, want mention of name_ky", result.Errors[0].Message)
	}
	if len(products.created) != 1 {
		t.Errorf("expected exactly 1 product created despite the bad row, got %d", len(products.created))
	}
}

// --- CSV: bad price ---

func TestImportProductsCSVBadPrice(t *testing.T) {
	deps, _, _, _, _ := baseDeps()

	csvData := "name_ru,name_ky,category,price\n" +
		"Кроссовки Nike,Найк кроссовкалар,sneakers,not-a-number\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 0 {
		t.Errorf("Imported = %d, want 0", result.Imported)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %+v, want exactly 1", result.Errors)
	}
	if !strings.Contains(result.Errors[0].Message, "цена") {
		t.Errorf("Message = %q, want a price-related message", result.Errors[0].Message)
	}
}

func TestImportProductsCSVNegativePrice(t *testing.T) {
	deps, _, _, _, _ := baseDeps()

	csvData := "name_ru,name_ky,category,price\n" +
		"Кроссовки Nike,Найк кроссовкалар,sneakers,-10\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 0 || len(result.Errors) != 1 {
		t.Fatalf("result = %+v, want 0 imported / 1 error", result)
	}
}

// --- CSV: unknown category ---

func TestImportProductsCSVUnknownCategory(t *testing.T) {
	deps, _, _, _, _ := baseDeps()

	csvData := "name_ru,name_ky,category,price\n" +
		"Кроссовки Nike,Найк кроссовкалар,does-not-exist,4999\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 0 {
		t.Errorf("Imported = %d, want 0", result.Imported)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Message, "does-not-exist") {
		t.Fatalf("Errors = %+v, want a category-not-found message naming the category", result.Errors)
	}
}

// --- CSV: mixed valid + invalid batch ---

func TestImportProductsCSVMixedBatch(t *testing.T) {
	deps, _, products, _, _ := baseDeps()

	csvData := "name_ru,name_ky,category,price\n" +
		"OK Row 1,ОК1,sneakers,1000\n" +
		"Bad Row,ОК2,unknown-category,1000\n" +
		"OK Row 2,ОК3,sneakers,2000\n" +
		",ОК4,sneakers,3000\n" + // missing name_ru
		"OK Row 3,ОК5,sneakers,bad-price\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 2 {
		t.Errorf("Imported = %d, want 2", result.Imported)
	}
	if len(result.Errors) != 3 {
		t.Fatalf("Errors = %+v, want exactly 3", result.Errors)
	}
	if len(products.created) != 2 {
		t.Errorf("expected 2 products actually created, got %d", len(products.created))
	}
	// Rows are 1-based counting the header, so the three bad data rows are
	// file lines 3, 5, 6.
	wantRows := map[int]bool{3: true, 5: true, 6: true}
	for _, e := range result.Errors {
		if !wantRows[e.Row] {
			t.Errorf("unexpected error row %d: %+v", e.Row, e)
		}
	}
}

// --- variant creation ---

func TestImportProductsCreatesVariantWhenSizeAndColorPresent(t *testing.T) {
	deps, _, _, variants, _ := baseDeps()

	csvData := "name_ru,name_ky,category,price,size,color,sku\n" +
		"Кроссовки,Кроссовкалар,sneakers,4999,42,black,SKU-1\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 1 || len(result.Errors) != 0 {
		t.Fatalf("result = %+v, want 1 imported / no errors", result)
	}
	if len(variants.created) != 1 {
		t.Fatalf("expected 1 variant created, got %d", len(variants.created))
	}
	v := variants.created[0]
	if v.Size != "42" || v.Color != "black" || v.SKU == nil || *v.SKU != "SKU-1" {
		t.Errorf("variant input = %+v, unexpected", v)
	}
}

func TestImportProductsRejectsSizeWithoutColor(t *testing.T) {
	deps, _, _, variants, _ := baseDeps()

	csvData := "name_ru,name_ky,category,price,size\n" +
		"Кроссовки,Кроссовкалар,sneakers,4999,42\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 0 || len(result.Errors) != 1 {
		t.Fatalf("result = %+v, want 0 imported / 1 error", result)
	}
	if len(variants.created) != 0 {
		t.Errorf("expected no variant created, got %d", len(variants.created))
	}
}

func TestImportProductsRejectsBadPriceOverride(t *testing.T) {
	deps, _, _, _, _ := baseDeps()

	csvData := "name_ru,name_ky,category,price,size,color,price_override\n" +
		"Кроссовки,Кроссовкалар,sneakers,4999,42,black,not-a-number\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 0 || len(result.Errors) != 1 {
		t.Fatalf("result = %+v, want 0 imported / 1 error", result)
	}
}

// --- optional per-point stock ---

func TestImportProductsSetsStockWhenColumnPresent(t *testing.T) {
	deps, _, _, _, stock := baseDeps()

	csvData := "name_ru,name_ky,category,price,size,color,stock:point-1\n" +
		"Кроссовки,Кроссовкалар,sneakers,4999,42,black,10\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 1 || len(result.Errors) != 0 {
		t.Fatalf("result = %+v, want 1 imported / no errors", result)
	}
	if len(stock.upserts) != 1 || stock.upserts[0].PointID != "point-1" || stock.upserts[0].Quantity != 10 {
		t.Errorf("stock upserts = %+v, unexpected", stock.upserts)
	}
}

func TestImportProductsSkipsStockWithoutVariant(t *testing.T) {
	deps, _, _, _, stock := baseDeps()

	// No size/color columns -> no variant -> stock column is meaningless
	// and must not be attempted (there's nothing to attach stock to).
	csvData := "name_ru,name_ky,category,price,stock:point-1\n" +
		"Кроссовки,Кроссовкалар,sneakers,4999,10\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 1 || len(result.Errors) != 0 {
		t.Fatalf("result = %+v, want 1 imported / no errors", result)
	}
	if len(stock.upserts) != 0 {
		t.Errorf("expected no stock upserts, got %+v", stock.upserts)
	}
}

func TestImportProductsStockFailureIsWarningNotRowFailure(t *testing.T) {
	deps, _, products, _, stock := baseDeps()
	stock.failFor["bad-point"] = true

	csvData := "name_ru,name_ky,category,price,size,color,stock:bad-point\n" +
		"Кроссовки,Кроссовкалар,sneakers,4999,42,black,10\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The product itself was created successfully (the core requirement),
	// so the row still counts as imported even though the optional stock
	// step failed — the failure is surfaced as an error entry (a warning),
	// not as a dropped row.
	if result.Imported != 1 {
		t.Errorf("Imported = %d, want 1 (product creation is the core requirement)", result.Imported)
	}
	if len(products.created) != 1 {
		t.Errorf("expected the product to have actually been created, got %d", len(products.created))
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Message, "bad-point") {
		t.Fatalf("Errors = %+v, want one warning naming the failed point", result.Errors)
	}
}

func TestImportProductsNilStockDepsSkipsStockColumns(t *testing.T) {
	deps, _, _, _, _ := baseDeps()
	deps.Stock = nil

	csvData := "name_ru,name_ky,category,price,size,color,stock:point-1\n" +
		"Кроссовки,Кроссовкалар,sneakers,4999,42,black,10\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 1 || len(result.Errors) != 0 {
		t.Fatalf("result = %+v, want 1 imported / no errors", result)
	}
}

// --- blank rows ---

func TestImportProductsSkipsBlankRows(t *testing.T) {
	deps, _, products, _, _ := baseDeps()

	csvData := "name_ru,name_ky,category,price\n" +
		"Кроссовки,Кроссовкалар,sneakers,4999\n" +
		",,,\n" +
		"\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 1 || len(result.Errors) != 0 {
		t.Fatalf("result = %+v, want 1 imported / no errors (blank rows skipped silently)", result)
	}
	if len(products.created) != 1 {
		t.Errorf("expected 1 product created, got %d", len(products.created))
	}
}

// --- category id passthrough (not just slug) ---

func TestImportProductsAcceptsCategoryUUID(t *testing.T) {
	deps, _, products, _, _ := baseDeps()

	csvData := "name_ru,name_ky,category,price\n" +
		"Кроссовки,Кроссовкалар,11111111-1111-1111-1111-111111111111,4999\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 1 {
		t.Fatalf("result = %+v, want 1 imported", result)
	}
	if products.created[0].CategoryID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("CategoryID = %q, unexpected", products.created[0].CategoryID)
	}
}

// --- header quirks ---

func TestImportProductsCSVHandlesBOMAndCaseInsensitiveHeaders(t *testing.T) {
	deps, _, products, _, _ := baseDeps()

	bom := string(rune(0xFEFF))
	csvData := bom + "NAME_RU,Name_Ky,Category,PRICE\n" +
		"Кроссовки,Кроссовкалар,sneakers,4999\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 1 || len(result.Errors) != 0 {
		t.Fatalf("result = %+v, want 1 imported / no errors", result)
	}
	if len(products.created) != 1 {
		t.Fatalf("expected 1 product created, got %d", len(products.created))
	}
}

func TestImportProductsCSVToleratesShortTrailingRow(t *testing.T) {
	deps, _, products, _, _ := baseDeps()

	// Second row omits the trailing (optional) columns entirely, which a
	// hand-edited CSV can easily do.
	csvData := "name_ru,name_ky,category,price,brand\n" +
		"Кроссовки,Кроссовкалар,sneakers,4999,Nike\n" +
		"Тапки,Шиштер,sneakers,999\n"

	result, err := ImportProducts(context.Background(), strings.NewReader(csvData), ImportFormatCSV, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 2 || len(result.Errors) != 0 {
		t.Fatalf("result = %+v, want 2 imported / no errors", result)
	}
	if products.created[1].Brand != nil {
		t.Errorf("Brand = %v, want nil for the row that omitted it", products.created[1].Brand)
	}
}

// --- whole-file failures ---

func TestImportProductsEmptyCSVFileFails(t *testing.T) {
	deps, _, _, _, _ := baseDeps()

	_, err := ImportProducts(context.Background(), strings.NewReader(""), ImportFormatCSV, deps)
	assertAppErrStatus(t, err, 400)
}

func TestImportProductsMalformedCSVFails(t *testing.T) {
	deps, _, _, _, _ := baseDeps()

	// A bare quote inside an unquoted field is invalid CSV syntax
	// (encoding/csv returns csv.ErrBareQuote) — this is a structural parse
	// failure, not a per-row validation failure, so the whole import
	// request fails rather than reporting it as a RowError.
	badCSV := "name_ru,name_ky,category,price\n" +
		"Foo\"Bar,ОК,sneakers,4999\n"

	_, err := ImportProducts(context.Background(), strings.NewReader(badCSV), ImportFormatCSV, deps)
	assertAppErrStatus(t, err, 400)
}

func TestImportProductsInvalidXLSXFails(t *testing.T) {
	deps, _, _, _, _ := baseDeps()

	_, err := ImportProducts(context.Background(), strings.NewReader("not an xlsx file"), ImportFormatXLSX, deps)
	assertAppErrStatus(t, err, 400)
}

// --- XLSX ---

// buildXLSX writes rows (first row = header) to an in-memory .xlsx file and
// returns its bytes, for round-tripping through ImportProducts without a
// fixture file on disk.
func buildXLSX(t *testing.T, rows [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheet := f.GetSheetName(0)
	for r, row := range rows {
		for c, val := range row {
			cell, err := excelize.CoordinatesToCellName(c+1, r+1)
			if err != nil {
				t.Fatalf("CoordinatesToCellName: %v", err)
			}
			if err := f.SetCellStr(sheet, cell, val); err != nil {
				t.Fatalf("SetCellStr: %v", err)
			}
		}
	}

	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	return buf.Bytes()
}

func TestImportProductsXLSXAllValid(t *testing.T) {
	deps, _, products, _, _ := baseDeps()

	data := buildXLSX(t, [][]string{
		{"name_ru", "name_ky", "category", "price"},
		{"Кроссовки Nike", "Найк кроссовкалар", "sneakers", "4999"},
		{"Кроссовки Adidas", "Адидас кроссовкалар", "sneakers", "3999.5"},
	})

	result, err := ImportProducts(context.Background(), bytes.NewReader(data), ImportFormatXLSX, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 2 || len(result.Errors) != 0 {
		t.Fatalf("result = %+v, want 2 imported / no errors", result)
	}
	if len(products.created) != 2 {
		t.Fatalf("expected 2 products created, got %d", len(products.created))
	}
}

func TestImportProductsXLSXMixedBatch(t *testing.T) {
	deps, _, _, _, _ := baseDeps()

	data := buildXLSX(t, [][]string{
		{"name_ru", "name_ky", "category", "price"},
		{"OK", "ОК", "sneakers", "1000"},
		{"Bad", "ОК2", "unknown-category", "1000"},
	})

	result, err := ImportProducts(context.Background(), bytes.NewReader(data), ImportFormatXLSX, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 1 {
		t.Errorf("Imported = %d, want 1", result.Imported)
	}
	if len(result.Errors) != 1 || result.Errors[0].Row != 3 {
		t.Fatalf("Errors = %+v, want exactly 1 at row 3", result.Errors)
	}
}

func TestImportProductsXLSXEmptySheetFails(t *testing.T) {
	deps, _, _, _, _ := baseDeps()

	f := excelize.NewFile()
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	_ = f.Close()

	_, err := ImportProducts(context.Background(), bytes.NewReader(buf.Bytes()), ImportFormatXLSX, deps)
	assertAppErrStatus(t, err, 400)
}

// --- format detection ---

func TestDetectImportFormat(t *testing.T) {
	cases := []struct {
		name        string
		filename    string
		contentType string
		wantFormat  ImportFormat
		wantOK      bool
	}{
		{"csv extension", "products.csv", "", ImportFormatCSV, true},
		{"CSV extension uppercase", "products.CSV", "", ImportFormatCSV, true},
		{"xlsx extension", "products.xlsx", "", ImportFormatXLSX, true},
		{"xlsx extension uppercase", "products.XLSX", "", ImportFormatXLSX, true},
		{"extension wins over conflicting content-type", "products.csv", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ImportFormatCSV, true},
		{"no extension, csv content-type", "upload", "text/csv", ImportFormatCSV, true},
		{"no extension, csv content-type with charset", "upload", "text/csv; charset=utf-8", ImportFormatCSV, true},
		{"no extension, xlsx content-type", "upload", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ImportFormatXLSX, true},
		{"application/csv content-type", "upload", "application/csv", ImportFormatCSV, true},
		{"unrecognized extension and content-type", "products.txt", "text/plain", 0, false},
		{"no filename, no content-type", "", "", 0, false},
		{"legacy xls extension unsupported", "products.xls", "", 0, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			format, ok := DetectImportFormat(c.filename, c.contentType)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && format != c.wantFormat {
				t.Errorf("format = %v, want %v", format, c.wantFormat)
			}
		})
	}
}
