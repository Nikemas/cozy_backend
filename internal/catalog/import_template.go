package catalog

import (
	"bytes"

	"github.com/xuri/excelize/v2"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/locales"
)

// TemplateCategory is one row of the template's category reference sheet.
type TemplateCategory struct {
	Slug   string
	NameRu string
	NameKy string
}

// templateColumns are the template's columns in order. Each one's header
// and hint for the instructions sheet live in locales/admin.*.yaml under
// admin.import.tpl.col.<name> / admin.import.tpl.hint.<name>, so the
// template comes in the admin's language; required columns get a "*"
// (normalizeHeader strips it). Every language's header is also an import
// alias (headerAliases), so a filled-in template of either language
// imports.
var templateColumns = []struct{ col, name string }{
	{colModelCode, "model_code"},
	{colNameRu, "name_ru"},
	{colNameKy, "name_ky"},
	{colCategory, "category"},
	{colBrand, "brand"},
	{colPrice, "price"},
	{colSize, "size"},
	{colColor, "color"},
	{colSKU, "sku"},
	{colQuantity, "quantity"},
	{colDescriptionRu, "description_ru"},
	{colDescriptionKy, "description_ky"},
}

// templateLangs are the languages the template is offered in.
var templateLangs = []string{i18n.LangRU, i18n.LangKY}

// tplText returns an admin.import.tpl.* text in lang.
func tplText(lang, key string) string {
	return locales.AdminBundle().T(lang, "admin.import.tpl."+key)
}

// isRequiredColumn reports whether col must be present in an import file.
func isRequiredColumn(col string) bool {
	for _, rc := range requiredColumns {
		if rc.key == col {
			return true
		}
	}
	return false
}

// templateHeaders returns the template's header row in lang.
func templateHeaders(lang string) []string {
	out := make([]string, len(templateColumns))
	for i, c := range templateColumns {
		h := tplText(lang, "col."+c.name)
		if isRequiredColumn(c.col) {
			h += "*"
		}
		out[i] = h
	}
	return out
}

// templateExample is a model with three sizes plus a single-size model.
var templateExample = [][]any{
	{"CZ-1001", "Кроссовки Cozy Run", "Cozy Run кроссовкалары", "Кроссовки", "Cozy", 4990, "39", "Белый", "CZ-1001-39-WH", 5, "Лёгкие беговые кроссовки", ""},
	{"CZ-1001", "", "", "", "", "", "40", "Белый", "CZ-1001-40-WH", 3, "", ""},
	{"CZ-1001", "", "", "", "", 5290, "41", "Белый", "CZ-1001-41-WH", 0, "", ""},
	{"CZ-2002", "Ботинки Cozy Winter", "", "Ботинки", "Cozy", 7490, "42", "Чёрный", "CZ-2002-42-BK", 2, "", ""},
}

// BuildImportTemplate renders the downloadable import template in lang
// ("ru"/"ky"; anything else is Russian): the products sheet (headers + an
// example to overwrite), the instructions sheet and, when categories is
// non-empty, a categories sheet with their slugs and names.
func BuildImportTemplate(lang string, categories []TemplateCategory) ([]byte, error) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	main := tplText(lang, "sheet_products")
	if err := f.SetSheetName(f.GetSheetName(0), main); err != nil {
		return nil, err
	}
	bold, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"EDEDED"}},
	})
	if err != nil {
		return nil, err
	}

	headers := templateHeaders(lang)
	header := make([]any, len(headers))
	for i, h := range headers {
		header[i] = h
	}
	if err := f.SetSheetRow(main, "A1", &header); err != nil {
		return nil, err
	}
	for i, example := range templateExample {
		row := append([]any(nil), example...)
		if len(categories) > 0 && row[3] != "" {
			row[3] = categories[min(i/3, len(categories)-1)].NameRu // an existing category, so the example validates
		}
		cell, _ := excelize.CoordinatesToCellName(1, i+2)
		if err := f.SetSheetRow(main, cell, &row); err != nil {
			return nil, err
		}
	}
	last, _ := excelize.ColumnNumberToName(len(templateColumns))
	if err := f.SetCellStyle(main, "A1", last+"1", bold); err != nil {
		return nil, err
	}
	if err := f.SetColWidth(main, "A", last, 16); err != nil {
		return nil, err
	}
	if err := f.SetColWidth(main, "B", "B", 28); err != nil {
		return nil, err
	}
	if err := f.SetPanes(main, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return nil, err
	}

	help := tplText(lang, "sheet_help")
	if _, err := f.NewSheet(help); err != nil {
		return nil, err
	}
	rows := [][]any{
		{tplText(lang, "help_column"), tplText(lang, "help_what")},
		{"", tplText(lang, "help_rows")},
		{"", tplText(lang, "help_check")},
	}
	for i, c := range templateColumns {
		rows = append(rows, []any{headers[i], tplText(lang, "hint."+c.name)})
	}
	for i, row := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetSheetRow(help, cell, &row); err != nil {
			return nil, err
		}
	}
	if err := f.SetCellStyle(help, "A1", "B1", bold); err != nil {
		return nil, err
	}
	if err := f.SetColWidth(help, "A", "A", 18); err != nil {
		return nil, err
	}
	if err := f.SetColWidth(help, "B", "B", 110); err != nil {
		return nil, err
	}

	if len(categories) > 0 {
		cats := tplText(lang, "sheet_categories")
		if _, err := f.NewSheet(cats); err != nil {
			return nil, err
		}
		if err := f.SetSheetRow(cats, "A1", &[]any{"slug", tplText(lang, "cat_name_ru"), tplText(lang, "cat_name_ky")}); err != nil {
			return nil, err
		}
		for i, c := range categories {
			cell, _ := excelize.CoordinatesToCellName(1, i+2)
			if err := f.SetSheetRow(cats, cell, &[]any{c.Slug, c.NameRu, c.NameKy}); err != nil {
				return nil, err
			}
		}
		if err := f.SetCellStyle(cats, "A1", "C1", bold); err != nil {
			return nil, err
		}
		if err := f.SetColWidth(cats, "A", "C", 28); err != nil {
			return nil, err
		}
	}

	f.SetActiveSheet(0)
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
