package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// pgCheckViolation: a CHECK constraint failed (e.g. stock.quantity >= 0).
const pgCheckViolation = "23514"

type stockSet struct {
	pointID string
	qty     int
}

// plannedRow is one validated data row.
type plannedRow struct {
	line       int
	hasVariant bool
	variant    ImportVariant
	stock      []stockSet
	err        string // validation error; non-empty fails the whole model
}

// plannedModel is one product to import: rows grouped by article, or by
// brand + name + category.
type plannedModel struct {
	label   string
	product ImportProduct
	rows    []*plannedRow
}

func (m *plannedModel) failed() bool { return m.firstErrLine() != 0 }

func (m *plannedModel) firstErrLine() int {
	for _, r := range m.rows {
		if r.err != "" {
			return r.line
		}
	}
	return 0
}

// fail attributes msg to the row at line (the first row if line is not in m).
func (m *plannedModel) fail(line int, msg string) {
	for _, r := range m.rows {
		if r.line == line {
			r.err = msg
			return
		}
	}
	m.rows[0].err = msg
}

type categoryLookup struct {
	id  string
	err string
}

// planner validates rows and groups them into models. It only reads from
// the store (category and point lookups, cached per distinct value).
type planner struct {
	ctx        context.Context
	store      ImportStore
	pointID    string // target of the quantity column; "" = none available
	usedPoint  bool
	categories map[string]categoryLookup
	points     map[string]bool
}

// rawRow is a row after cell-level parsing, before grouping.
type rawRow struct {
	row                   importRow
	modelCode, nameRu     string
	nameKy, category      string
	brand, descRu, descKy string
	price, priceOverride  *float64
	priceBad              bool // the price cell was present but invalid
	planned               *plannedRow
	errs                  []string
}

func (p *planner) plan(rows []importRow) ([]*plannedModel, error) {
	var models []*plannedModel
	byKey := map[string]*plannedModel{}
	groups := map[*plannedModel][]*rawRow{}
	skuLines := map[string]int{}

	for _, row := range rows {
		if row.isBlank() {
			continue
		}
		rr, err := p.parseRow(row)
		if err != nil {
			return nil, err
		}

		if sku := rr.planned.variant.SKU; sku != "" && rr.planned.hasVariant {
			if first, dup := skuLines[sku]; dup {
				rr.errs = append(rr.errs, fmt.Sprintf("SKU %q уже встречается в строке %d", sku, first))
			} else {
				skuLines[sku] = row.line
			}
		}

		key := groupKey(rr)
		if key == "" {
			// No way to tell which model the row belongs to: report it alone.
			if rr.nameRu == "" {
				rr.errs = append(rr.errs, "не указано название (или артикул модели)")
			}
			if rr.category == "" {
				rr.errs = append(rr.errs, "не указана категория (или артикул модели)")
			}
			rr.planned.err = strings.Join(rr.errs, "; ")
			models = append(models, &plannedModel{label: firstNonEmpty(rr.modelCode, rr.nameRu), rows: []*plannedRow{rr.planned}})
			continue
		}
		m, ok := byKey[key]
		if !ok {
			m = &plannedModel{}
			byKey[key] = m
			models = append(models, m)
		}
		m.rows = append(m.rows, rr.planned)
		groups[m] = append(groups[m], rr)
	}

	for _, m := range models {
		if rs, ok := groups[m]; ok {
			p.assemble(m, rs)
		}
	}
	return models, nil
}

// groupKey is the model identity of a row: its article when present,
// otherwise brand + name + category ("" if name or category is missing).
func groupKey(rr *rawRow) string {
	if rr.modelCode != "" {
		return "a\x00" + strings.ToLower(rr.modelCode)
	}
	if rr.nameRu == "" || rr.category == "" {
		return ""
	}
	return "n\x00" + strings.ToLower(rr.brand) + "\x00" + strings.ToLower(rr.nameRu) + "\x00" + strings.ToLower(rr.category)
}

// parseRow validates the cells of one row. Cell problems are collected in
// rr.errs; a non-nil error means the store failed (abort the import).
func (p *planner) parseRow(row importRow) (*rawRow, error) {
	rr := &rawRow{
		row:       row,
		modelCode: normalizeText(row.get(colModelCode)),
		nameRu:    normalizeText(row.get(colNameRu)),
		nameKy:    normalizeText(row.get(colNameKy)),
		category:  normalizeText(row.get(colCategory)),
		brand:     normalizeText(row.get(colBrand)),
		descRu:    strings.TrimSpace(row.get(colDescriptionRu)),
		descKy:    strings.TrimSpace(row.get(colDescriptionKy)),
		planned:   &plannedRow{line: row.line},
	}

	if v := row.get(colPrice); v != "" {
		f, err := parsePrice(v)
		if err != nil {
			rr.errs = append(rr.errs, err.Error())
			rr.priceBad = true
		} else {
			rr.price = &f
		}
	}
	if v := row.get(colPriceOverride); v != "" {
		f, err := parsePrice(v)
		if err != nil {
			rr.errs = append(rr.errs, "цена варианта: "+err.Error())
		} else {
			rr.priceOverride = &f
		}
	}

	size := normalizeSize(row.get(colSize))
	color := normalizeText(row.get(colColor))
	sku := strings.TrimSpace(row.get(colSKU))
	switch {
	case size != "" && color != "":
		rr.planned.hasVariant = true
		rr.planned.variant = ImportVariant{Size: size, Color: color, SKU: sku}
	case size != "" || color != "":
		rr.errs = append(rr.errs, "для вариации нужны оба поля: размер и цвет")
	case sku != "":
		rr.errs = append(rr.errs, "SKU указан без размера и цвета")
	}

	if v := row.get(colQuantity); v != "" {
		qty, err := parseQuantity(v)
		switch {
		case err != nil:
			rr.errs = append(rr.errs, err.Error())
		case !rr.planned.hasVariant:
			rr.errs = append(rr.errs, "остаток указан без размера и цвета")
		case p.pointID == "":
			rr.errs = append(rr.errs, "нет активной точки продаж, чтобы записать остаток")
		default:
			p.usedPoint = true
			rr.planned.stock = append(rr.planned.stock, stockSet{pointID: p.pointID, qty: qty})
		}
	}

	var stockCols []string
	for col := range row.fields {
		if strings.HasPrefix(col, stockPointColumnPrefix) {
			stockCols = append(stockCols, col)
		}
	}
	sort.Strings(stockCols)
	for _, col := range stockCols {
		v := row.fields[col]
		pointID := strings.TrimPrefix(col, stockPointColumnPrefix)
		if v == "" {
			continue
		}
		qty, err := parseQuantity(v)
		if err != nil {
			rr.errs = append(rr.errs, fmt.Sprintf("остаток по точке %s: %s", pointID, err.Error()))
			continue
		}
		if !rr.planned.hasVariant {
			rr.errs = append(rr.errs, "остаток указан без размера и цвета")
			continue
		}
		ok, err := p.activePoint(pointID)
		if err != nil {
			return nil, err
		}
		if !ok {
			rr.errs = append(rr.errs, fmt.Sprintf("точка продаж %s не найдена или отключена", pointID))
			continue
		}
		rr.planned.stock = append(rr.planned.stock, stockSet{pointID: pointID, qty: qty})
	}
	return rr, nil
}

func (p *planner) activePoint(id string) (bool, error) {
	if ok, cached := p.points[id]; cached {
		return ok, nil
	}
	ok := false
	if _, err := uuid.Parse(id); err == nil {
		var err error
		if ok, err = p.store.ActivePoint(p.ctx, id); err != nil {
			return false, err
		}
	}
	p.points[id] = ok
	return ok, nil
}

func (p *planner) category(raw string) categoryLookup {
	key := strings.ToLower(raw)
	if c, ok := p.categories[key]; ok {
		return c
	}
	id, err := p.store.ResolveCategory(p.ctx, raw)
	c := categoryLookup{id: id}
	if err != nil {
		c = categoryLookup{err: dbErrorMessage(err)}
	}
	p.categories[key] = c
	return c
}

// assemble builds m's product from its rows (first non-empty value of each
// field wins), derives variant price overrides and checks model-level
// rules. Problems land on the row they concern.
func (p *planner) assemble(m *plannedModel, rs []*rawRow) {
	first := rs[0]
	pick := func(get func(*rawRow) string) string {
		for _, r := range rs {
			if v := get(r); v != "" {
				return v
			}
		}
		return ""
	}
	prod := ImportProduct{
		ModelCode:     first.modelCode,
		NameRu:        pick(func(r *rawRow) string { return r.nameRu }),
		NameKy:        pick(func(r *rawRow) string { return r.nameKy }),
		Brand:         pick(func(r *rawRow) string { return r.brand }),
		DescriptionRu: pick(func(r *rawRow) string { return r.descRu }),
		DescriptionKy: pick(func(r *rawRow) string { return r.descKy }),
	}
	m.label = firstNonEmpty(prod.ModelCode, prod.NameRu)

	if prod.NameRu == "" {
		first.errs = append(first.errs, "не указано название")
	}
	if cat := pick(func(r *rawRow) string { return r.category }); cat == "" {
		first.errs = append(first.errs, "не указана категория")
	} else if c := p.category(cat); c.err != "" {
		first.errs = append(first.errs, c.err)
	} else {
		prod.CategoryID = c.id
	}

	var base *float64
	for _, r := range rs {
		if r.price != nil {
			base = r.price
			break
		}
	}
	if base == nil {
		if !hasBadPrice(rs) {
			first.errs = append(first.errs, "не указана цена")
		}
	} else {
		prod.BasePrice = *base
	}
	m.product = prod

	sizes := map[string]int{}
	for _, r := range rs {
		pr := r.planned
		if !pr.hasVariant {
			if len(rs) > 1 {
				r.errs = append(r.errs, "в модели несколько строк — у каждой нужны размер и цвет")
			}
			continue
		}
		k := strings.ToLower(pr.variant.Size) + "\x00" + strings.ToLower(pr.variant.Color)
		if line, dup := sizes[k]; dup {
			r.errs = append(r.errs, fmt.Sprintf("размер %s / цвет %s уже есть в строке %d", pr.variant.Size, pr.variant.Color, line))
		} else {
			sizes[k] = r.row.line
		}
		switch {
		case r.priceOverride != nil:
			pr.variant.PriceOverride = r.priceOverride
		case r.price != nil && base != nil && *r.price != *base:
			pr.variant.PriceOverride = r.price
		}
	}

	for _, r := range rs {
		if len(r.errs) > 0 {
			r.planned.err = strings.Join(r.errs, "; ")
		}
	}
}

func hasBadPrice(rs []*rawRow) bool {
	for _, r := range rs {
		if r.priceBad {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
