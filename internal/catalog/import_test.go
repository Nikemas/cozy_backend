package catalog

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// buildXLSX writes rows (row 0 = header) into a one-sheet workbook. A cell
// value may be a string or a number (written as a numeric cell).
func buildXLSX(t *testing.T, rows [][]string) []byte {
	t.Helper()
	anyRows := make([][]any, len(rows))
	for i, r := range rows {
		anyRows[i] = make([]any, len(r))
		for j, v := range r {
			anyRows[i][j] = v
		}
	}
	return buildXLSXAny(t, anyRows)
}

func buildXLSXAny(t *testing.T, rows [][]any) []byte {
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
			if err := f.SetCellValue(sheet, cell, val); err != nil {
				t.Fatalf("SetCellValue: %v", err)
			}
		}
	}

	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	return buf.Bytes()
}

func runCSV(t *testing.T, s *memStore, csv string, opts ImportOptions) *ImportResult {
	t.Helper()
	res, err := ImportProducts(context.Background(), strings.NewReader(csv), ImportFormatCSV, s, opts)
	if err != nil {
		t.Fatalf("ImportProducts: %v", err)
	}
	return res
}

func runXLSX(t *testing.T, s *memStore, data []byte, opts ImportOptions) *ImportResult {
	t.Helper()
	res, err := ImportProducts(context.Background(), bytes.NewReader(data), ImportFormatXLSX, s, opts)
	if err != nil {
		t.Fatalf("ImportProducts: %v", err)
	}
	return res
}

func rowStatus(t *testing.T, res *ImportResult, line int) ImportRowResult {
	t.Helper()
	for _, r := range res.Rows {
		if r.Row == line {
			return r
		}
	}
	t.Fatalf("no report row %d in %+v", line, res.Rows)
	return ImportRowResult{}
}

func wantSummary(t *testing.T, got, want ImportSummary) {
	t.Helper()
	if got != want {
		t.Errorf("summary = %+v\n          want %+v", got, want)
	}
}

// modelSheet is the typical price list: one model, three sizes, Russian
// headers as in the template.
var modelSheet = [][]string{
	{"Артикул", "Название*", "Категория*", "Бренд", "Цена*", "Размер", "Цвет", "SKU", "Остаток", "Описание"},
	{"CZ-1", "Кроссовки Run", "Кроссовки", "Cozy", "4990", "39", "Белый", "CZ-1-39", "5", "Лёгкие"},
	{"CZ-1", "", "", "", "", "40", "Белый", "CZ-1-40", "3", ""},
	{"CZ-1", "", "", "", "5290", "41", "Белый", "CZ-1-41", "0", ""},
	{"CZ-2", "Ботинки Winter", "boots", "Cozy", "7490", "42", "Чёрный", "CZ-2-42", "2", ""},
}

func TestImportGroupsSizesIntoOneProductPerModel(t *testing.T) {
	s := newMemStore()
	res := runXLSX(t, s, buildXLSX(t, modelSheet), ImportOptions{})

	wantSummary(t, res.Summary, ImportSummary{Rows: 4, Created: 4, ProductsCreated: 2, VariantsCreated: 4})
	if len(s.st.products) != 2 || len(s.st.variants) != 4 {
		t.Fatalf("products=%d variants=%d, want 2 and 4", len(s.st.products), len(s.st.variants))
	}
	run, ok := s.productByName("Кроссовки Run")
	if !ok {
		t.Fatal("product Кроссовки Run not created")
	}
	if run.ModelCode != "CZ-1" || run.BasePrice != 4990 || run.CategoryID != testCatSneakers || run.NameKy != "Кроссовки Run" {
		t.Errorf("product = %+v", run.ImportProduct)
	}
	if n := len(s.variantsOf(run.id)); n != 3 {
		t.Errorf("CZ-1 has %d variants, want 3", n)
	}
	// Size 41 costs more than the model's base price -> price_override.
	v41, _ := s.variantBySKU("CZ-1-41")
	if v41.PriceOverride == nil || *v41.PriceOverride != 5290 {
		t.Errorf("CZ-1-41 override = %v, want 5290", v41.PriceOverride)
	}
	v40, _ := s.variantBySKU("CZ-1-40")
	if v40.PriceOverride != nil {
		t.Errorf("CZ-1-40 override = %v, want nil", *v40.PriceOverride)
	}
	// Stock went to the default point.
	if q := s.st.stock[[2]string{v40.id, testPointA}]; q != 3 {
		t.Errorf("stock CZ-1-40 @A = %d, want 3", q)
	}
	if res.PointID == nil || *res.PointID != testPointA {
		t.Errorf("PointID = %v, want default point A", res.PointID)
	}
	if res.Imported != 2 || len(res.Errors) != 0 {
		t.Errorf("legacy imported=%d errors=%+v", res.Imported, res.Errors)
	}
}

func TestImportGroupsByBrandNameCategoryWithoutArticle(t *testing.T) {
	s := newMemStore()
	csv := "name_ru,category,brand,price,size,color\n" +
		"Кеды,sneakers,Cozy,1500,38,red\n" +
		"кеды,Sneakers,cozy,1500,39,red\n" + // same model, different case
		"Кеды,sneakers,Other,1500,38,red\n" // different brand -> different model
	res := runCSV(t, s, csv, ImportOptions{})
	wantSummary(t, res.Summary, ImportSummary{Rows: 3, Created: 3, ProductsCreated: 2, VariantsCreated: 3})
}

func TestReimportUpdatesInsteadOfDuplicating(t *testing.T) {
	s := newMemStore()
	data := buildXLSX(t, modelSheet)
	runXLSX(t, s, data, ImportOptions{})

	res := runXLSX(t, s, data, ImportOptions{})
	wantSummary(t, res.Summary, ImportSummary{Rows: 4, Updated: 4, ProductsUpdated: 2, VariantsUpdated: 4})
	if len(s.st.products) != 2 || len(s.st.variants) != 4 {
		t.Fatalf("after re-import products=%d variants=%d, want 2 and 4", len(s.st.products), len(s.st.variants))
	}

	// Change price, stock and description: the same rows are updated.
	changed := [][]string{
		modelSheet[0],
		{"CZ-1", "Кроссовки Run", "Кроссовки", "Cozy", "4500", "39", "Белый", "CZ-1-39", "7", "Новое описание"},
		{"CZ-1", "", "", "", "", "40", "Белый", "CZ-1-40", "1", ""},
		{"CZ-1", "", "", "", "", "41", "Белый", "CZ-1-41", "0", ""},
		{"CZ-1", "", "", "", "", "42", "Белый", "CZ-1-42", "4", ""}, // new size
	}
	res = runXLSX(t, s, buildXLSX(t, changed), ImportOptions{})
	wantSummary(t, res.Summary, ImportSummary{Rows: 4, Created: 1, Updated: 3, ProductsUpdated: 1, VariantsCreated: 1, VariantsUpdated: 3})
	run, _ := s.productByName("Кроссовки Run")
	if run.BasePrice != 4500 || run.DescriptionRu != "Новое описание" {
		t.Errorf("product after update = %+v", run.ImportProduct)
	}
	v41, _ := s.variantBySKU("CZ-1-41")
	if v41.PriceOverride != nil {
		t.Errorf("CZ-1-41 override = %v, want cleared (row price now empty)", *v41.PriceOverride)
	}
	v39, _ := s.variantBySKU("CZ-1-39")
	if q := s.st.stock[[2]string{v39.id, testPointA}]; q != 7 {
		t.Errorf("stock CZ-1-39 = %d, want 7", q)
	}
	if n := len(s.variantsOf(run.id)); n != 4 {
		t.Errorf("variants = %d, want 4", n)
	}
}

func TestReimportMatchesProductBySKUWhenNoArticle(t *testing.T) {
	s := newMemStore()
	runCSV(t, s, "name_ru,category,price,size,color,sku\nКеды,sneakers,1500,38,red,K-38\n", ImportOptions{})
	// Renamed in the price list, but the SKU identifies the model.
	res := runCSV(t, s, "name_ru,category,price,size,color,sku\nКеды летние,sneakers,1600,38,red,K-38\n", ImportOptions{})
	wantSummary(t, res.Summary, ImportSummary{Rows: 1, Updated: 1, ProductsUpdated: 1, VariantsUpdated: 1})
	if len(s.st.products) != 1 {
		t.Fatalf("products = %d, want 1", len(s.st.products))
	}
	if _, ok := s.productByName("Кеды летние"); !ok {
		t.Error("product not renamed")
	}
}

func TestReimportMatchesVariantBySizeColorWithoutSKU(t *testing.T) {
	s := newMemStore()
	csv := "name_ru,category,price,size,color,quantity\nКеды,sneakers,1500,38,Red,2\n"
	runCSV(t, s, csv, ImportOptions{})
	res := runCSV(t, s, "name_ru,category,price,size,color,quantity\nКеды,sneakers,1500,38,red,9\n", ImportOptions{})
	wantSummary(t, res.Summary, ImportSummary{Rows: 1, Updated: 1, ProductsUpdated: 1, VariantsUpdated: 1})
	if len(s.st.variants) != 1 {
		t.Fatalf("variants = %d, want 1", len(s.st.variants))
	}
}

func TestInvalidRowSkipsWholeModelOnly(t *testing.T) {
	s := newMemStore()
	csv := "article,name_ru,category,price,size,color,sku\n" +
		"A1,Модель A,sneakers,1000,38,red,A1-38\n" +
		"A1,,,,39,red,A1-39\n" +
		"A1,,,-5,40,red,A1-40\n" + // negative price -> whole A1 not imported
		"B1,Модель B,sneakers,2000,40,blue,B1-40\n"
	res := runCSV(t, s, csv, ImportOptions{})

	wantSummary(t, res.Summary, ImportSummary{Rows: 4, Created: 1, Skipped: 2, Errors: 1, ProductsCreated: 1, VariantsCreated: 1})
	if r := rowStatus(t, res, 4); r.Status != RowStatusError || !strings.Contains(r.Message, "отрицательной") {
		t.Errorf("row 4 = %+v", r)
	}
	if r := rowStatus(t, res, 2); r.Status != RowStatusSkipped || !strings.Contains(r.Message, "строке 4") {
		t.Errorf("row 2 = %+v", r)
	}
	if _, ok := s.productByName("Модель A"); ok {
		t.Error("Модель A must not be created when one of its rows is invalid")
	}
	if len(res.Errors) != 3 || res.Errors[0].Row != 2 {
		t.Errorf("legacy errors = %+v", res.Errors)
	}
}

func TestDatabaseFailureRollsBackWholeModel(t *testing.T) {
	s := newMemStore()
	s.failCreateVariantAfter = 2 // second variant insert fails
	csv := "article,name_ru,category,price,size,color\n" +
		"A1,Модель A,sneakers,1000,38,red\n" +
		"A1,,,,39,red\n"
	res := runCSV(t, s, csv, ImportOptions{})

	if len(s.st.products) != 0 || len(s.st.variants) != 0 {
		t.Fatalf("orphans left: products=%d variants=%d", len(s.st.products), len(s.st.variants))
	}
	wantSummary(t, res.Summary, ImportSummary{Rows: 2, Skipped: 1, Errors: 1})
	if r := rowStatus(t, res, 3); r.Status != RowStatusError || !strings.Contains(r.Message, "внутренняя ошибка") {
		t.Errorf("row 3 = %+v", r)
	}
}

func TestDryRunWritesNothingAndReportsTheSame(t *testing.T) {
	s := newMemStore()
	data := buildXLSX(t, modelSheet)

	dry := runXLSX(t, s, data, ImportOptions{DryRun: true})
	if !dry.DryRun {
		t.Error("DryRun flag not echoed")
	}
	if len(s.st.products) != 0 || len(s.st.variants) != 0 || len(s.st.stock) != 0 {
		t.Fatalf("dry run wrote data: %d products, %d variants, %d stock", len(s.st.products), len(s.st.variants), len(s.st.stock))
	}
	real := runXLSX(t, s, data, ImportOptions{})
	if dry.Summary != real.Summary {
		t.Errorf("dry summary %+v != real %+v", dry.Summary, real.Summary)
	}
}

func TestDryRunReportsDatabaseConflicts(t *testing.T) {
	s := newMemStore()
	runCSV(t, s, "article,name_ru,category,price,size,color,sku\nA1,Модель A,sneakers,1000,38,red,X-1\n", ImportOptions{})
	// X-1 now belongs to A1; claiming it for B1 is only detectable with the DB.
	res := runCSV(t, s, "article,name_ru,category,price,size,color,sku\nB1,Модель B,sneakers,1000,38,red,X-1\n", ImportOptions{DryRun: true})
	if r := rowStatus(t, res, 2); r.Status != RowStatusError || !strings.Contains(r.Message, "другого товара") {
		t.Errorf("row 2 = %+v", r)
	}
}

func TestPriceAndQuantityValidation(t *testing.T) {
	cases := []struct {
		price, qty string
		wantMsg    string
	}{
		{"NaN", "", "цена не число"},
		{"Inf", "", "цена не число"},
		{"+Inf", "", "цена не число"},
		{"-1", "", "отрицательной"},
		{"0x1p3", "", "цена не число"},
		{"1e3", "", "цена не число"},
		{"abc", "", "цена не число"},
		{"100000000", "", "слишком большая цена"},
		{"100", "-1", "отрицательным"},
		{"100", "1.5", "целым числом"},
		{"100", "NaN", "целым числом"},
		{"100", "2000000", "слишком большой остаток"},
	}
	for _, c := range cases {
		t.Run(c.price+"/"+c.qty, func(t *testing.T) {
			s := newMemStore()
			res := runCSV(t, s, "name_ru,category,price,size,color,quantity\nКеды,sneakers,"+c.price+",38,red,"+c.qty+"\n", ImportOptions{})
			r := rowStatus(t, res, 2)
			if r.Status != RowStatusError || !strings.Contains(r.Message, c.wantMsg) {
				t.Errorf("row = %+v, want error containing %q", r, c.wantMsg)
			}
			if len(s.st.products) != 0 {
				t.Error("product created despite invalid row")
			}
		})
	}
}

func TestPriceFormatsAccepted(t *testing.T) {
	for raw, want := range map[string]float64{
		"4990":        4990,
		"4 990":       4990,
		`"4990,50"`:   4990.5,
		"4990.555":    4990.56,
		"0":           0,
		`"4 990 сом"`: 4990,
		"\u00a04990 ": 4990,
	} {
		s := newMemStore()
		res := runCSV(t, s, "name_ru,category,price\nКеды,sneakers,"+raw+"\n", ImportOptions{})
		if res.Summary.Created != 1 {
			t.Errorf("%q: %+v", raw, res.Rows)
			continue
		}
		p, _ := s.productByName("Кеды")
		if p.BasePrice != want {
			t.Errorf("%q: price = %v, want %v", raw, p.BasePrice, want)
		}
	}
}

func TestRequiredFieldsAndVariantRules(t *testing.T) {
	cases := []struct {
		name, csv, wantMsg string
	}{
		{"no name", "name_ru,category,price\n,sneakers,100\n", "не указано название"},
		{"no category", "name_ru,category,price\nКеды,,100\n", "не указана категория"},
		{"no price", "name_ru,category,price\nКеды,sneakers,\n", "не указана цена"},
		{"unknown category", "name_ru,category,price\nКеды,sandals,100\n", "не найдена"},
		{"size without color", "name_ru,category,price,size,color\nКеды,sneakers,100,38,\n", "оба поля"},
		{"sku without variant", "name_ru,category,price,sku\nКеды,sneakers,100,K1\n", "SKU указан без"},
		{"qty without variant", "name_ru,category,price,quantity\nКеды,sneakers,100,4\n", "остаток указан без"},
		{"article without name", "article,name_ru,category,price\nA1,,sneakers,100\n", "не указано название"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := runCSV(t, newMemStore(), c.csv, ImportOptions{})
			r := rowStatus(t, res, 2)
			if r.Status != RowStatusError || !strings.Contains(r.Message, c.wantMsg) {
				t.Errorf("row = %+v, want error containing %q", r, c.wantMsg)
			}
		})
	}
}

func TestDuplicatesInsideFileAreRejected(t *testing.T) {
	s := newMemStore()
	csv := "article,name_ru,category,price,size,color,sku\n" +
		"A1,Модель A,sneakers,1000,38,red,S-1\n" +
		"A1,,,,38,RED,S-2\n" + // same size/color as row 2
		"B1,Модель B,sneakers,1000,40,red,S-1\n" // SKU of row 2
	res := runCSV(t, s, csv, ImportOptions{})
	if r := rowStatus(t, res, 3); r.Status != RowStatusError || !strings.Contains(r.Message, "уже есть в строке 2") {
		t.Errorf("row 3 = %+v", r)
	}
	if r := rowStatus(t, res, 4); r.Status != RowStatusError || !strings.Contains(r.Message, "встречается в строке 2") {
		t.Errorf("row 4 = %+v", r)
	}
	if len(s.st.products) != 0 {
		t.Errorf("products = %d, want 0", len(s.st.products))
	}
}

func TestSizeAndColorAreNormalized(t *testing.T) {
	s := newMemStore()
	res := runCSV(t, s, "name_ru,category,price,size,color\n  Кеды  ,sneakers,100,\" 42,5 \",\"  тёмно   синий \"\n", ImportOptions{})
	if res.Summary.Created != 1 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	p, _ := s.productByName("Кеды")
	vs := s.variantsOf(p.id)
	if len(vs) != 1 || vs[0].Size != "42.5" || vs[0].Color != "тёмно синий" {
		t.Errorf("variants = %+v", vs)
	}
}

func TestStockTargetPoint(t *testing.T) {
	csv := "name_ru,category,price,size,color,sku,quantity\nКеды,sneakers,100,38,red,K1,6\n"

	t.Run("explicit point", func(t *testing.T) {
		s := newMemStore()
		res := runCSV(t, s, csv, ImportOptions{PointID: testPointB})
		v, _ := s.variantBySKU("K1")
		if q := s.st.stock[[2]string{v.id, testPointB}]; q != 6 {
			t.Errorf("stock @B = %d, want 6", q)
		}
		if _, ok := s.st.stock[[2]string{v.id, testPointA}]; ok {
			t.Error("stock written to A too")
		}
		if res.PointID == nil || *res.PointID != testPointB {
			t.Errorf("PointID = %v", res.PointID)
		}
	})
	t.Run("inactive point rejected", func(t *testing.T) {
		_, err := ImportProducts(context.Background(), strings.NewReader(csv), ImportFormatCSV, newMemStore(), ImportOptions{PointID: testPointOff})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != "invalid_point" {
			t.Errorf("err = %v, want invalid_point", err)
		}
	})
	t.Run("garbage point rejected", func(t *testing.T) {
		_, err := ImportProducts(context.Background(), strings.NewReader(csv), ImportFormatCSV, newMemStore(), ImportOptions{PointID: "x'; drop"})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != "invalid_point" {
			t.Errorf("err = %v, want invalid_point", err)
		}
	})
	t.Run("no active point", func(t *testing.T) {
		s := newMemStore()
		s.defaultPoint = ""
		res := runCSV(t, s, csv, ImportOptions{})
		if r := rowStatus(t, res, 2); r.Status != RowStatusError || !strings.Contains(r.Message, "нет активной точки") {
			t.Errorf("row = %+v", r)
		}
		// Without quantities the same file imports fine.
		res = runCSV(t, s, "name_ru,category,price,size,color\nКеды,sneakers,100,38,red\n", ImportOptions{})
		if res.Summary.Created != 1 || res.PointID != nil {
			t.Errorf("summary = %+v point=%v", res.Summary, res.PointID)
		}
	})
}

func TestPerPointStockColumns(t *testing.T) {
	s := newMemStore()
	csv := "name_ru,category,price,size,color,sku,stock:" + testPointA + ",stock:" + testPointB + "\n" +
		"Кеды,sneakers,100,38,red,K1,4,\n" +
		"Кроссовки,sneakers,100,38,red,K2,1,2\n"
	res := runCSV(t, s, csv, ImportOptions{})
	if res.Summary.Created != 2 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	v, _ := s.variantBySKU("K2")
	if s.st.stock[[2]string{v.id, testPointA}] != 1 || s.st.stock[[2]string{v.id, testPointB}] != 2 {
		t.Errorf("stock = %+v", s.st.stock)
	}

	res = runCSV(t, s, "name_ru,category,price,size,color,stock:"+testPointOff+"\nКеды,sneakers,100,38,red,4\n", ImportOptions{})
	if r := rowStatus(t, res, 2); r.Status != RowStatusError || !strings.Contains(r.Message, "не найдена или отключена") {
		t.Errorf("row = %+v", r)
	}
}

func TestSemicolonCSVWithBOMAndRussianHeaders(t *testing.T) {
	s := newMemStore()
	csv := "\ufeffНазвание;Категория;Цена *;Размер;Цвет\n" +
		"Кеды;Кроссовки;1 500,00;38;red\n"
	res := runCSV(t, s, csv, ImportOptions{})
	if res.Summary.Created != 1 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	if p, _ := s.productByName("Кеды"); p.BasePrice != 1500 {
		t.Errorf("price = %v", p.BasePrice)
	}
}

func TestFileLevelErrors(t *testing.T) {
	cases := []struct {
		name     string
		format   ImportFormat
		data     []byte
		wantCode string
	}{
		{"empty csv", ImportFormatCSV, nil, "empty_file"},
		{"malformed csv", ImportFormatCSV, []byte("name_ru,category,price\n\"Кеды,sneakers,100\n"), "invalid_csv"},
		{"missing columns", ImportFormatCSV, []byte("name_ru,size\nКеды,38\n"), "missing_columns"},
		{"duplicate column", ImportFormatCSV, []byte("name_ru,название,category,price\n"), "duplicate_column"},
		{"not a workbook", ImportFormatXLSX, []byte("not a zip"), "invalid_xlsx"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ImportProducts(context.Background(), bytes.NewReader(c.data), c.format, newMemStore(), ImportOptions{})
			var ae *apperr.AppError
			if !errors.As(err, &ae) || ae.Code != c.wantCode {
				t.Errorf("err = %v, want %s", err, c.wantCode)
			}
		})
	}
}

func TestXLSXNumericCellsAndBlankRows(t *testing.T) {
	s := newMemStore()
	data := buildXLSXAny(t, [][]any{
		{"name_ru", "category", "price", "size", "color", "sku", "quantity"},
		{"Кеды", "sneakers", 4990.5, 38, "red", 4600000000017, 3},
		{},
		{"", "", "", "", "", "", ""},
		{"Кеды", "sneakers", 4990.5, 39.5, "red", "K-39", 2.0},
	})
	res := runXLSX(t, s, data, ImportOptions{})
	wantSummary(t, res.Summary, ImportSummary{Rows: 2, Created: 2, ProductsCreated: 1, VariantsCreated: 2})
	v, ok := s.variantBySKU("4600000000017")
	if !ok || v.Size != "38" {
		t.Errorf("numeric SKU/size variant = %+v ok=%v (variants %+v)", v, ok, s.st.variants)
	}
	if rowStatus(t, res, 5).Size != "39.5" {
		t.Errorf("row 5 = %+v", rowStatus(t, res, 5))
	}
}

func TestLegacyEnglishFileStillImports(t *testing.T) {
	// The pre-2026-09 column set, one product per row.
	s := newMemStore()
	csv := "name_ru,name_ky,category,price,brand,description_ru,size,color,sku,price_override\n" +
		"Кроссовки Nike,Найк,sneakers,4999,Nike,Описание,42,black,N-42,5200\n" +
		"Кроссовки Adidas,Адидас," + testCatSneakers + ",3999.50,,,,,,\n"
	res := runCSV(t, s, csv, ImportOptions{})
	wantSummary(t, res.Summary, ImportSummary{Rows: 2, Created: 2, ProductsCreated: 2, VariantsCreated: 1})
	v, _ := s.variantBySKU("N-42")
	if v.PriceOverride == nil || *v.PriceOverride != 5200 {
		t.Errorf("explicit price_override lost: %v", v.PriceOverride)
	}
	if p, _ := s.productByName("Кроссовки Nike"); p.NameKy != "Найк" || p.Brand != "Nike" {
		t.Errorf("product = %+v", p.ImportProduct)
	}
}

func TestTemplateImportsCleanly(t *testing.T) {
	data, err := BuildImportTemplate([]TemplateCategory{{Slug: "sneakers", NameRu: "Кроссовки", NameKy: "Кроссовкалар"}, {Slug: "boots", NameRu: "Ботинки"}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.GetSheetList(); len(got) != 3 || got[0] != "Товары" {
		t.Errorf("sheets = %v", got)
	}
	_ = f.Close()

	s := newMemStore()
	res := runXLSX(t, s, data, ImportOptions{DryRun: true})
	wantSummary(t, res.Summary, ImportSummary{Rows: 4, Created: 4, ProductsCreated: 2, VariantsCreated: 4})

	// Without categories (empty DB) the template still builds.
	if _, err := BuildImportTemplate(nil); err != nil {
		t.Fatal(err)
	}
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
