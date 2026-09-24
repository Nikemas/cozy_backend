// product_form_parse.go turns a submitted product form into a
// productSaveInput (and, on failure, the view model to re-render it
// with) — pure functions over url.Values, unit-testable without a
// database or an *http.Request.
//
// Form contract (product_form.gohtml):
//
//	variant_key[] / variant_id[] / variant_size[] / variant_color[]
//	    parallel arrays, one entry per Вариации row. variant_key is the
//	    variant id for an existing row, "n<N>" for a row added in the
//	    browser.
//	qty_<key>_<pointID>   the quantity typed into that row × point cell
//	orig_<key>_<pointID>  the quantity the cell was rendered with ("" when
//	                      no stock row existed); only present for cells
//	                      rendered by the server
//
// Only cells whose qty differs from orig become stockCellChanges; an
// untouched cell is never written, so a stale form can't overwrite stock
// that changed after it was opened.
package admin

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

// maxStockQty caps one cell — a typo guard ("1000000" instead of "10"),
// well above any real per-point quantity for a shoe shop.
const maxStockQty = 100000

// StockPointVM is one point-of-sale column of the Вариации stock matrix.
type StockPointVM struct {
	ID       string
	Name     string
	Inactive bool
}

// StockCellVM is one (variant row × point) stock input.
type StockCellVM struct {
	PointID   string
	PointName string
	Name      string // qty_<key>_<pointID>
	OrigName  string // orig_<key>_<pointID>
	Value     string // what the input shows
	Orig      string // what the cell was rendered with ("" = no stock row)
	HasOrig   bool   // render the orig_ hidden input at all
	Conflict  bool   // stock changed concurrently; Value/Orig refreshed from the DB
	Invalid   bool   // Value failed validation
}

func stockFieldName(key, pointID string) string { return "qty_" + key + "_" + pointID }
func stockOrigFieldName(key, pointID string) string {
	return "orig_" + key + "_" + pointID
}

// parseStockQty parses one stock cell. Blank, non-integer, negative or
// absurdly large values are errors — never silently 0.
func parseStockQty(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("пустое количество")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("количество должно быть целым числом")
	}
	if n < 0 {
		return 0, fmt.Errorf("количество не может быть отрицательным")
	}
	if n > maxStockQty {
		return 0, fmt.Errorf("слишком большое количество")
	}
	return n, nil
}

// parsePrice parses the base-price field ("4 500" and "4500,50" accepted).
// A blank or unparsable value is an error rather than a silent 0.
func parsePrice(s string) (float64, error) {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), " ", ""))
	s = strings.ReplaceAll(s, ",", ".")
	if s == "" {
		return 0, fmt.Errorf("укажите цену")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("цена должна быть числом")
	}
	if v < 0 {
		return 0, fmt.Errorf("цена не может быть отрицательной")
	}
	return v, nil
}

// errStaleForm marks a hidden orig_ value that isn't a valid quantity —
// only possible with a tampered or corrupted form.
var errStaleForm = errors.New("форма повреждена — обновите страницу")

const staleFormMessage = "Форма повреждена — обновите страницу"

// parsedCell is one stock cell as submitted.
type parsedCell struct {
	Qty     int  // the submitted quantity (or the original one when skipped)
	Orig    *int // nil = no stock row existed when the form was rendered
	Changed bool // Qty differs from Orig: this cell must be written
}

// parseStockCell interprets one stock input (raw, present) and its hidden
// original (origRaw, hasOrig). A cell that wasn't submitted, or was left
// blank where no stock row existed, is unchanged; a cleared cell that did
// have stock is an error (never a silent 0).
func parseStockCell(raw string, present bool, origRaw string, hasOrig bool) (parsedCell, error) {
	raw = strings.TrimSpace(raw)
	origRaw = strings.TrimSpace(origRaw)

	var orig *int
	if hasOrig && origRaw != "" {
		n, err := strconv.Atoi(origRaw)
		if err != nil || n < 0 {
			return parsedCell{}, errStaleForm
		}
		orig = &n
	}

	if !present || (raw == "" && orig == nil) {
		pc := parsedCell{Orig: orig}
		if orig != nil {
			pc.Qty = *orig
		}
		return pc, nil
	}

	qty, err := parseStockQty(raw)
	if err != nil {
		return parsedCell{}, err
	}
	unchanged := (orig == nil && qty == 0) || (orig != nil && *orig == qty)
	return parsedCell{Qty: qty, Orig: orig, Changed: !unchanged}, nil
}

// parsedProductForm is parseProductForm's result: Input is ready for
// productStore.Save when Errs is empty; Rows re-renders the Вариации table
// exactly as submitted (with Invalid marks) otherwise.
type parsedProductForm struct {
	Input productSaveInput
	Rows  []VariantRowVM
	Errs  []string
}

// parseProductForm reads the whole form. points are the matrix columns
// the form was rendered with (every point of sale).
func parseProductForm(form url.Values, productID string, isActive bool, points []StockPointVM) parsedProductForm {
	var out parsedProductForm
	addErr := func(msg string) {
		for _, e := range out.Errs {
			if e == msg {
				return
			}
		}
		out.Errs = append(out.Errs, msg)
	}

	price, err := parsePrice(form.Get("base_price"))
	if err != nil {
		addErr("Цена: " + err.Error())
	}
	out.Input = productSaveInput{
		ProductID: productID,
		Product: catalog.ProductInput{
			CategoryID:    form.Get("category_id"),
			NameRu:        strings.TrimSpace(form.Get("name_ru")),
			NameKy:        strings.TrimSpace(form.Get("name_ky")),
			DescriptionRu: nilIfEmpty(form.Get("description_ru")),
			DescriptionKy: nilIfEmpty(form.Get("description_ky")),
			Brand:         nilIfEmpty(form.Get("brand")),
			BasePrice:     price,
			IsActive:      isActive,
		},
	}

	keys := form["variant_key"]
	ids := form["variant_id"]
	sizes := form["variant_size"]
	colors := form["variant_color"]
	seenKeys := map[string]bool{}
	for i := range sizes {
		size := strings.TrimSpace(formAt(sizes, i))
		color := strings.TrimSpace(formAt(colors, i))
		key := strings.TrimSpace(formAt(keys, i))
		if key == "" {
			key = fmt.Sprintf("row%d", i)
		}
		if seenKeys[key] {
			addErr("Форма повреждена (повтор строки вариации) — обновите страницу")
			continue
		}
		seenKeys[key] = true

		id := strings.TrimSpace(formAt(ids, i))
		if size == "" && color == "" && id == "" && !rowHasStock(form, key, points) {
			continue // an empty row the staff member added and never filled in
		}
		if size == "" || color == "" {
			addErr("У каждой вариации должны быть размер и цвет")
		}

		row := VariantRowVM{Key: key, ID: id, Size: size, Color: color}
		total := 0
		for _, p := range points {
			cell := StockCellVM{
				PointID:   p.ID,
				PointName: p.Name,
				Name:      stockFieldName(key, p.ID),
				OrigName:  stockOrigFieldName(key, p.ID),
			}
			raw, present := formValue(form, cell.Name)
			origRaw, hasOrig := formValue(form, cell.OrigName)
			cell.Value = strings.TrimSpace(raw)
			cell.Orig = strings.TrimSpace(origRaw)
			cell.HasOrig = hasOrig

			pc, err := parseStockCell(raw, present, origRaw, hasOrig)
			if err != nil {
				if errors.Is(err, errStaleForm) {
					addErr(staleFormMessage)
				} else {
					addErr(fmt.Sprintf("Остаток %s / %s, «%s»: %s", orEmpty(size), orEmpty(color), p.Name, err.Error()))
				}
				cell.Invalid = true
				row.Cells = append(row.Cells, cell)
				continue
			}
			total += pc.Qty
			row.Cells = append(row.Cells, cell)
			if pc.Changed {
				out.Input.Stock = append(out.Input.Stock, stockCellChange{RowKey: key, PointID: p.ID, Qty: pc.Qty, Orig: pc.Orig})
			}
		}
		row.Qty = total
		row.BadgeLbl, row.BadgeFG, row.BadgeBG = stockChip(total)

		out.Rows = append(out.Rows, row)
		out.Input.Variants = append(out.Input.Variants, variantRowInput{Key: key, ID: id, Size: size, Color: color})
	}

	imageKeys := form["image_object_key"]
	imageColors := form["image_color"]
	for i, k := range imageKeys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out.Input.Images = append(out.Input.Images, catalog.ImageInput{ObjectKey: k, SortOrder: i, Color: nilIfEmpty(formAt(imageColors, i))})
	}

	return out
}

func orEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// formValue returns form[name][0] and whether the field was submitted.
func formValue(form url.Values, name string) (string, bool) {
	v, ok := form[name]
	if !ok || len(v) == 0 {
		return "", false
	}
	return v[0], true
}

func rowHasStock(form url.Values, key string, points []StockPointVM) bool {
	for _, p := range points {
		if v := strings.TrimSpace(form.Get(stockFieldName(key, p.ID))); v != "" && v != "0" {
			return true
		}
	}
	return false
}

// markStockConflicts refreshes the conflicting cells of rows (as
// submitted) to the current stored quantity — both the visible value and
// the hidden original — so re-submitting the form after reviewing them
// saves against the fresh values instead of conflicting forever.
func markStockConflicts(rows []VariantRowVM, conflicts []stockConflict) {
	type cellKey struct{ row, point string }
	byCell := make(map[cellKey]stockConflict, len(conflicts))
	for _, c := range conflicts {
		byCell[cellKey{c.RowKey, c.PointID}] = c
	}
	for i := range rows {
		total := 0
		for j := range rows[i].Cells {
			cell := &rows[i].Cells[j]
			if c, ok := byCell[cellKey{rows[i].Key, cell.PointID}]; ok {
				cell.Value = strconv.Itoa(c.Current)
				cell.HasOrig = true
				cell.Orig = ""
				if c.Exists {
					cell.Orig = strconv.Itoa(c.Current)
				}
				cell.Conflict = true
			}
			if n, err := strconv.Atoi(cell.Value); err == nil {
				total += n
			}
		}
		rows[i].Qty = total
		rows[i].BadgeLbl, rows[i].BadgeFG, rows[i].BadgeBG = stockChip(total)
	}
}

// buildVariantRows is the edit page's initial Вариации matrix: one row per
// variant, one cell per point, each cell's value = its stored quantity.
func buildVariantRows(variants []catalog.Variant, entries []catalog.StockEntry, points []StockPointVM) []VariantRowVM {
	type cellKey struct{ variant, point string }
	stored := make(map[cellKey]int, len(entries))
	for _, e := range entries {
		stored[cellKey{e.VariantID, e.PointID}] = e.Quantity
	}

	rows := make([]VariantRowVM, 0, len(variants))
	for _, v := range variants {
		row := VariantRowVM{Key: v.ID, ID: v.ID, Size: v.Size, Color: v.Color}
		for _, p := range points {
			cell := StockCellVM{
				PointID:   p.ID,
				PointName: p.Name,
				Name:      stockFieldName(v.ID, p.ID),
				OrigName:  stockOrigFieldName(v.ID, p.ID),
				HasOrig:   true,
				Value:     "0",
			}
			if q, ok := stored[cellKey{v.ID, p.ID}]; ok {
				cell.Value = strconv.Itoa(q)
				cell.Orig = cell.Value
				row.Qty += q
			}
			row.Cells = append(row.Cells, cell)
		}
		row.BadgeLbl, row.BadgeFG, row.BadgeBG = stockChip(row.Qty)
		rows = append(rows, row)
	}
	return rows
}
