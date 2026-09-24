// audit_hooks.go builds audit-journal entries (internal/audit) for the
// admin panel's writes: product form saves (product, variants, stock
// cells), the Остатки screen, row/bulk product actions, categories,
// points of sale and staff accounts. Order status changes are journaled by
// internal/orders' order_status_history and are not repeated here.
package admin

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/points"
)

// productSnapshot is a product row as it was before a form save.
type productSnapshot struct {
	CategoryID    string
	NameRu        string
	NameKy        string
	DescriptionRu *string
	DescriptionKy *string
	Brand         *string
	BasePrice     float64
}

func loadProductSnapshotTx(ctx context.Context, tx *sql.Tx, productID string) (*productSnapshot, error) {
	const q = `
		SELECT category_id, name_ru, name_ky, description_ru, description_ky, brand, base_price
		FROM products WHERE id = $1`
	var s productSnapshot
	err := tx.QueryRowContext(ctx, q, productID).Scan(&s.CategoryID, &s.NameRu, &s.NameKy,
		&s.DescriptionRu, &s.DescriptionKy, &s.Brand, &s.BasePrice)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// variantSnapshot is a variant row as it was before a form save.
type variantSnapshot struct {
	Size  string
	Color string
}

func loadVariantSnapshotsTx(ctx context.Context, tx *sql.Tx, productID string) (map[string]variantSnapshot, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, size, color FROM product_variants WHERE product_id = $1`, productID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]variantSnapshot{}
	for rows.Next() {
		var id string
		var v variantSnapshot
		if err := rows.Scan(&id, &v.Size, &v.Color); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// productFieldLabels names product fields (locale keys) in journal
// summaries and the journal page's details column.
var productFieldLabels = map[string]string{
	"category_id":    "admin.audit.field.category",
	"name_ru":        "admin.audit.field.name_ru",
	"name_ky":        "admin.audit.field.name_ky",
	"description_ru": "admin.audit.field.description_ru",
	"description_ky": "admin.audit.field.description_ky",
	"brand":          "admin.audit.field.brand",
	"base_price":     "admin.audit.field.price",
}

// productChanges diffs old against the submitted input: only fields that
// actually changed, as {field: {from, to}}.
func productChanges(old *productSnapshot, in catalog.ProductInput) map[string]any {
	out := map[string]any{}
	add := func(field string, from, to any, changed bool) {
		if changed {
			out[field] = audit.Change{From: from, To: to}
		}
	}
	add("category_id", old.CategoryID, in.CategoryID, old.CategoryID != in.CategoryID)
	add("name_ru", old.NameRu, in.NameRu, old.NameRu != in.NameRu)
	add("name_ky", old.NameKy, in.NameKy, old.NameKy != in.NameKy)
	add("description_ru", deref(old.DescriptionRu), deref(in.DescriptionRu), deref(old.DescriptionRu) != deref(in.DescriptionRu))
	add("description_ky", deref(old.DescriptionKy), deref(in.DescriptionKy), deref(old.DescriptionKy) != deref(in.DescriptionKy))
	add("brand", deref(old.Brand), deref(in.Brand), deref(old.Brand) != deref(in.Brand))
	add("base_price", old.BasePrice, in.BasePrice, old.BasePrice != in.BasePrice)
	return out
}

// changedFieldsLabel lists changed fields for a summary, in a stable order.
// Summaries are stored with the entry, so they are always written in
// Russian (ruTr); only the journal page's own labels follow the viewer's
// language.
func changedFieldsLabel(changes map[string]any) string {
	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	labels := make([]string, len(keys))
	for i, k := range keys {
		if l, ok := productFieldLabels[k]; ok {
			labels[i] = ruTr.T(l)
		} else {
			labels[i] = k
		}
	}
	return strings.Join(labels, ", ")
}

// productSaveEntries builds the product + variant entries of one form
// save. old/oldVariants are nil when the "before" read was skipped or
// failed (then an update is journaled without a field diff).
func productSaveEntries(productID string, in productSaveInput, old *productSnapshot, oldVariants map[string]variantSnapshot, idByKey map[string]string) []audit.Entry {
	name := in.Product.NameRu
	var out []audit.Entry
	if in.ProductID == "" {
		out = append(out, audit.Entry{
			Action: audit.ActionProductCreate, EntityType: audit.EntityProduct, EntityID: productID,
			Summary: fmt.Sprintf("Создан товар «%s»", name),
			Details: map[string]any{"name_ru": name, "category_id": in.Product.CategoryID, "base_price": in.Product.BasePrice,
				"brand": deref(in.Product.Brand)},
		})
	} else if old != nil {
		if changes := productChanges(old, in.Product); len(changes) > 0 {
			out = append(out, audit.Entry{
				Action: audit.ActionProductUpdate, EntityType: audit.EntityProduct, EntityID: productID,
				Summary: fmt.Sprintf("Изменён товар «%s»: %s", name, changedFieldsLabel(changes)),
				Details: changes,
			})
		}
	} else {
		out = append(out, audit.Entry{
			Action: audit.ActionProductUpdate, EntityType: audit.EntityProduct, EntityID: productID,
			Summary: fmt.Sprintf("Изменён товар «%s»", name),
		})
	}

	kept := map[string]bool{}
	for _, row := range in.Variants {
		id := idByKey[row.Key]
		prev, existed := oldVariants[id]
		if row.ID != "" && existed {
			kept[id] = true
			if prev.Size != row.Size || prev.Color != row.Color {
				out = append(out, audit.Entry{
					Action: audit.ActionVariantUpdate, EntityType: audit.EntityVariant, EntityID: id,
					Summary: fmt.Sprintf("«%s»: вариация %s / %s → %s / %s", name, prev.Size, prev.Color, row.Size, row.Color),
					Details: map[string]any{"product_id": productID,
						"size": audit.Change{From: prev.Size, To: row.Size}, "color": audit.Change{From: prev.Color, To: row.Color}},
				})
			}
			continue
		}
		if row.ID != "" && oldVariants == nil {
			continue // no snapshot: can't tell an edit from a no-op
		}
		out = append(out, audit.Entry{
			Action: audit.ActionVariantCreate, EntityType: audit.EntityVariant, EntityID: id,
			Summary: fmt.Sprintf("«%s»: добавлена вариация %s / %s", name, row.Size, row.Color),
			Details: map[string]any{"product_id": productID, "size": row.Size, "color": row.Color},
		})
	}
	deleted := make([]string, 0)
	for id := range oldVariants {
		if !kept[id] {
			deleted = append(deleted, id)
		}
	}
	sort.Strings(deleted)
	for _, id := range deleted {
		prev := oldVariants[id]
		out = append(out, audit.Entry{
			Action: audit.ActionVariantDelete, EntityType: audit.EntityVariant, EntityID: id,
			Summary: fmt.Sprintf("«%s»: удалена вариация %s / %s", name, prev.Size, prev.Color),
			Details: map[string]any{"product_id": productID, "size": prev.Size, "color": prev.Color},
		})
	}
	return out
}

// stockCellLabel is the product/variant/point naming for a stock entry.
type stockCellLabel struct {
	ProductID string
	Product   string
	Size      string
	Color     string
}

// stockEntriesTx builds one stock.update entry per applied cell (old →
// new), looking up product/variant/point names for the summary in two
// batch queries. Cells whose row was dropped from the submission, and
// cells whose value didn't actually change, are skipped.
func stockEntriesTx(ctx context.Context, tx *sql.Tx, idByKey map[string]string, changes []stockCellChange) ([]audit.Entry, error) {
	type cell struct {
		variantID, pointID string
		from, to           int
	}
	var cells []cell
	variantSet, pointSet := map[string]bool{}, map[string]bool{}
	for _, c := range changes {
		variantID, ok := idByKey[c.RowKey]
		if !ok {
			continue
		}
		from := 0
		if c.Orig != nil {
			from = *c.Orig
		}
		if from == c.Qty {
			continue
		}
		cells = append(cells, cell{variantID, c.PointID, from, c.Qty})
		variantSet[variantID] = true
		pointSet[c.PointID] = true
	}
	if len(cells) == 0 {
		return nil, nil
	}

	labels, err := variantLabelsTx(ctx, tx, keys(variantSet))
	if err != nil {
		return nil, err
	}
	points, err := pointNamesTx(ctx, tx, keys(pointSet))
	if err != nil {
		return nil, err
	}

	out := make([]audit.Entry, 0, len(cells))
	for _, c := range cells {
		l := labels[c.variantID]
		pointName := points[c.pointID]
		if pointName == "" {
			pointName = c.pointID
		}
		out = append(out, audit.Entry{
			Action: audit.ActionStockUpdate, EntityType: audit.EntityStock, EntityID: c.variantID,
			Summary: fmt.Sprintf("Остаток «%s» %s / %s, %s: %d → %d", l.Product, l.Size, l.Color, pointName, c.from, c.to),
			Details: map[string]any{"product_id": l.ProductID, "point_id": c.pointID, "point": pointName,
				"quantity": audit.Change{From: c.from, To: c.to}},
		})
	}
	return out, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func variantLabelsTx(ctx context.Context, tx *sql.Tx, variantIDs []string) (map[string]stockCellLabel, error) {
	const q = `
		SELECT v.id, p.id, p.name_ru, v.size, v.color
		FROM product_variants v JOIN products p ON p.id = v.product_id
		WHERE v.id = ANY($1)`
	rows, err := tx.QueryContext(ctx, q, variantIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]stockCellLabel{}
	for rows.Next() {
		var id string
		var l stockCellLabel
		if err := rows.Scan(&id, &l.ProductID, &l.Product, &l.Size, &l.Color); err != nil {
			return nil, err
		}
		out[id] = l
	}
	return out, rows.Err()
}

func pointNamesTx(ctx context.Context, tx *sql.Tx, pointIDs []string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM points_of_sale WHERE id = ANY($1)`, pointIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// --- non-transactional actions (journaled after they succeed) ---

// auditProductActive journals the row menu's Активировать/Деактивировать.
func (h *handlers) auditProductActive(ctx context.Context, id, name string, active bool) {
	action, verb := audit.ActionProductDeactivate, "деактивирован"
	if active {
		action, verb = audit.ActionProductActivate, "активирован"
	}
	h.audit.Record(ctx, audit.Entry{
		Action: action, EntityType: audit.EntityProduct, EntityID: id,
		Summary: fmt.Sprintf("Товар «%s» %s", name, verb),
		Details: map[string]any{"is_active": audit.Change{From: !active, To: active}},
	})
}

// auditProductDeleted journals the owner-only Удалить (a soft delete).
func (h *handlers) auditProductDeleted(ctx context.Context, id string) {
	h.audit.Record(ctx, audit.Entry{
		Action: audit.ActionProductDelete, EntityType: audit.EntityProduct, EntityID: id,
		Summary: "Товар удалён (скрыт из каталога)",
	})
}

// categoryName resolves a category's name for the journal (before a
// delete); "" when the journal is off or the lookup fails.
func (h *handlers) categoryName(ctx context.Context, id string) string {
	if !h.audit.Enabled() || h.categories == nil {
		return ""
	}
	tree, err := h.categories.Tree(ctx)
	if err != nil {
		return ""
	}
	var find func([]*catalog.Category) string
	find = func(cs []*catalog.Category) string {
		for _, c := range cs {
			if c.ID == id {
				return c.NameRu
			}
			if n := find(c.Children); n != "" {
				return n
			}
		}
		return ""
	}
	return find(tree)
}

// auditCategory journals a category create/update/delete from the
// Категории screen or the JSON API.
func (h *handlers) auditCategory(ctx context.Context, action, id, name string, in *catalog.CategoryInput) {
	h.audit.Record(ctx, categoryAuditEntry(action, id, name, in))
}

// categoryAuditEntry builds a category journal entry.
func categoryAuditEntry(action, id, name string, in *catalog.CategoryInput) audit.Entry {
	verb := map[string]string{
		audit.ActionCategoryCreate: "Создана",
		audit.ActionCategoryUpdate: "Изменена",
		audit.ActionCategoryDelete: "Удалена",
	}[action]
	e := audit.Entry{Action: action, EntityType: audit.EntityCategory, EntityID: id,
		Summary: strings.TrimSpace(fmt.Sprintf("%s категория «%s»", verb, name))}
	if in != nil {
		e.Details = map[string]any{"name_ru": in.NameRu, "name_ky": in.NameKy, "slug": in.Slug,
			"parent_id": deref(in.ParentID), "sort_order": in.SortOrder}
	}
	return e
}

// auditPoint journals a point of sale create/edit/toggle. before is nil
// for a create.
func (h *handlers) auditPoint(ctx context.Context, action, id string, before *points.Point, in points.PointInput) {
	e := audit.Entry{Action: action, EntityType: audit.EntityPoint, EntityID: id}
	switch action {
	case audit.ActionPointCreate:
		e.Summary = fmt.Sprintf("Создана точка «%s»", in.Name)
		e.Details = map[string]any{"name": in.Name, "address": in.Address}
	case audit.ActionPointActivate, audit.ActionPointDeactivate:
		verb := "отключена"
		if in.IsActive {
			verb = "включена"
		}
		e.Summary = fmt.Sprintf("Точка «%s» %s", in.Name, verb)
		e.Details = map[string]any{"is_active": audit.Change{From: !in.IsActive, To: in.IsActive}}
	default:
		e.Summary = fmt.Sprintf("Изменена точка «%s»", in.Name)
		d := map[string]any{}
		if before != nil && before.Name != in.Name {
			d["name"] = audit.Change{From: before.Name, To: in.Name}
		}
		if before != nil && before.Address != in.Address {
			d["address"] = audit.Change{From: before.Address, To: in.Address}
		}
		e.Details = d
	}
	h.audit.Record(ctx, e)
}

// auditStaff journals a staff account create/activate/deactivate/
// password reset. The password itself is never journaled.
func (h *handlers) auditStaff(ctx context.Context, action, id, name string, details map[string]any) {
	if name == "" && h.audit.Enabled() && h.staffSvc != nil {
		if list, err := h.staffSvc.ListStaff(ctx); err == nil {
			for _, s := range list {
				if s.ID == id {
					name = s.Name
				}
			}
		}
	}
	summary := map[string]string{
		audit.ActionStaffCreate:     "Создан сотрудник «%s»",
		audit.ActionStaffActivate:   "Сотрудник «%s» включён",
		audit.ActionStaffDeactivate: "Сотрудник «%s» отключён",
		audit.ActionStaffPassword:   "Сброшен пароль сотрудника «%s»",
	}[action]
	h.audit.Record(ctx, audit.Entry{Action: action, EntityType: audit.EntityStaff, EntityID: id,
		Summary: fmt.Sprintf(summary, name), Details: details})
}
