package catalog

import (
	"bytes"

	"github.com/xuri/excelize/v2"
)

// TemplateCategory is one row of the template's category reference sheet.
type TemplateCategory struct {
	Slug   string
	NameRu string
	NameKy string
}

// templateColumns are the template's headers in order, with a short hint
// for the instructions sheet. Required ones end in "*" (normalizeHeader
// strips it). Each caption is an alias in columnAliases.
var templateColumns = []struct{ header, hint string }{
	{"Артикул", "Артикул модели. Строки с одним артикулом — один товар (разные размеры/цвета). Если пусто — товар определяется по бренду + названию + категории."},
	{"Название*", "Название товара на русском."},
	{"Название (KY)", "Название на кыргызском. Если пусто — берётся русское (можно перевести потом в карточке товара)."},
	{"Категория*", "Название категории или её slug (см. лист «Категории»). Достаточно указать в первой строке модели."},
	{"Бренд", ""},
	{"Цена*", "Цена в сомах, число ≥ 0 (например 4990 или 4990,50). Достаточно указать в первой строке модели; если у размера цена другая — она станет ценой этой вариации."},
	{"Размер", "Размер вариации (например 38 или 42,5). Размер и цвет указываются вместе."},
	{"Цвет", "Цвет вариации."},
	{"SKU", "Уникальный код вариации (штрихкод). По нему повторный импорт обновляет вариацию, а не создаёт новую."},
	{"Остаток", "Целое число ≥ 0 — остаток на точке продаж, выбранной при импорте (заменяет текущий остаток)."},
	{"Описание", "Описание на русском. Пустая ячейка не стирает уже заполненное описание."},
	{"Описание (KY)", "Описание на кыргызском."},
}

// templateExample is a model with three sizes plus a single-size model.
var templateExample = [][]any{
	{"CZ-1001", "Кроссовки Cozy Run", "Cozy Run кроссовкалары", "Кроссовки", "Cozy", 4990, "39", "Белый", "CZ-1001-39-WH", 5, "Лёгкие беговые кроссовки", ""},
	{"CZ-1001", "", "", "", "", "", "40", "Белый", "CZ-1001-40-WH", 3, "", ""},
	{"CZ-1001", "", "", "", "", 5290, "41", "Белый", "CZ-1001-41-WH", 0, "", ""},
	{"CZ-2002", "Ботинки Cozy Winter", "", "Ботинки", "Cozy", 7490, "42", "Чёрный", "CZ-2002-42-BK", 2, "", ""},
}

// BuildImportTemplate renders the downloadable import template: the
// "Товары" sheet (headers + an example to overwrite), "Инструкция" and,
// when categories is non-empty, "Категории" with their slugs and names.
func BuildImportTemplate(categories []TemplateCategory) ([]byte, error) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	const main = "Товары"
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

	header := make([]any, len(templateColumns))
	for i, c := range templateColumns {
		header[i] = c.header
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

	const help = "Инструкция"
	if _, err := f.NewSheet(help); err != nil {
		return nil, err
	}
	rows := [][]any{
		{"Колонка", "Что указывать"},
		{"", "Одна строка = одна вариация (размер × цвет). Обязательные колонки отмечены *. Порядок колонок не важен, лишние колонки игнорируются."},
		{"", "Сначала нажмите «Проверить» — отчёт покажет, что будет создано/обновлено и какие строки с ошибками, без записи в базу."},
	}
	for _, c := range templateColumns {
		rows = append(rows, []any{c.header, c.hint})
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
		const cats = "Категории"
		if _, err := f.NewSheet(cats); err != nil {
			return nil, err
		}
		if err := f.SetSheetRow(cats, "A1", &[]any{"slug", "Название", "Название (KY)"}); err != nil {
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
