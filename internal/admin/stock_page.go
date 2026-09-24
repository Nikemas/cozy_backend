// stock_page.go implements the "Остатки" screen (GET/POST /admin/stock):
// one point of sale's stock for every variant, editable in place.
//
// It is the point_staff view of the catalog from the admin brief (§3:
// "свои заказы + остатки своей точки ... только количество на складе") —
// a point_staff member is always pinned to their own staff.PointID and
// never sees a point selector; owner/manager pick any point. Saving uses
// the same optimistic, changed-cells-only write as the product form
// (product_store.go), in one transaction.
package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/dbtx"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// stockPageSize is the number of variant rows per page.
const stockPageSize = 50

// stockPageRow is one variant with its stock at the selected point.
type stockPageRow struct {
	VariantID   string
	ProductID   string
	ProductName string
	Brand       string
	Size        string
	Color       string
	Quantity    int
	HasStockRow bool
}

// stockPageStore is the DB side of the screen (an interface so the
// handler can be tested with a fake).
type stockPageStore interface {
	List(ctx context.Context, pointID, query string, page int) ([]stockPageRow, int, error)
	Apply(ctx context.Context, changes []stockCellChange) error
}

type stockPageRepo struct {
	db *sql.DB
}

func newStockPageRepo(db *sql.DB) *stockPageRepo { return &stockPageRepo{db: db} }

// escapeLike escapes LIKE/ILIKE wildcards in user input.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// List returns active products' variants with their stock at pointID,
// optionally filtered by product name / brand / SKU, sorted by product.
func (r *stockPageRepo) List(ctx context.Context, pointID, query string, page int) ([]stockPageRow, int, error) {
	if page < 1 {
		page = 1
	}
	pattern := "%" + escapeLike(strings.TrimSpace(query)) + "%"
	const where = `
		FROM product_variants v
		JOIN products p ON p.id = v.product_id
		LEFT JOIN stock s ON s.variant_id = v.id AND s.point_id = $1
		WHERE p.is_active
		  AND ($2 = '%%' OR p.name_ru ILIKE $2 OR p.name_ky ILIKE $2 OR p.brand ILIKE $2 OR v.sku ILIKE $2)`

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) `+where, pointID, pattern).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := `SELECT v.id, p.id, p.name_ru, COALESCE(p.brand, ''), v.size, v.color, s.quantity ` + where + `
		ORDER BY p.name_ru, p.id, v.size, v.color
		LIMIT $3 OFFSET $4`
	rows, err := r.db.QueryContext(ctx, q, pointID, pattern, stockPageSize, (page-1)*stockPageSize)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var out []stockPageRow
	for rows.Next() {
		var row stockPageRow
		var qty sql.NullInt64
		if err := rows.Scan(&row.VariantID, &row.ProductID, &row.ProductName, &row.Brand, &row.Size, &row.Color, &qty); err != nil {
			return nil, 0, err
		}
		row.Quantity = int(qty.Int64)
		row.HasStockRow = qty.Valid
		out = append(out, row)
	}
	return out, total, rows.Err()
}

// Apply writes changes (RowKey = variant id) in one transaction, with the
// same optimistic check as the product form.
func (r *stockPageRepo) Apply(ctx context.Context, changes []stockCellChange) error {
	if len(changes) == 0 {
		return nil
	}
	idByKey := make(map[string]string, len(changes))
	for _, c := range changes {
		idByKey[c.RowKey] = c.RowKey
	}
	return dbtx.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		return applyStockChangesTx(ctx, tx, idByKey, changes)
	})
}

// --- view models ---

// PointOptionVM is one <option> of a point-of-sale select.
type PointOptionVM struct {
	ID       string
	Name     string
	Selected bool
}

// StockPageRowVM is one row of the Остатки table.
type StockPageRowVM struct {
	VariantID   string
	ProductName string
	Brand       string
	Size        string
	Color       string
	EditURL     string // product form link, "" for point_staff
	Cell        StockCellVM
	BadgeLbl    string
	BadgeFG     string
	BadgeBG     string
}

// StockPageData backs stock.gohtml.
type StockPageData struct {
	CanChoosePoint bool
	Points         []PointOptionVM
	PointID        string
	PointName      string
	NoPoint        string // why no point can be shown ("" when one is selected)

	Query      string
	Page       int
	Rows       []StockPageRowVM
	Empty      bool
	CountLabel string
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string

	Err string
}

func stockPageURL(pointID, query string, page int, canChoose bool) string {
	v := url.Values{}
	if canChoose && pointID != "" {
		v.Set("point", pointID)
	}
	if query != "" {
		v.Set("q", query)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return "/admin/stock"
	}
	return "/admin/stock?" + v.Encode()
}

// resolveStockPoint picks the point the page shows: point_staff is pinned
// to their own point (whatever ?point= says); owner/manager get the
// requested point if it exists, else the first active one.
func (h *handlers) resolveStockPoint(ctx context.Context, st *staff.Staff, requested string) (data StockPageData, err error) {
	t := trFromContext(ctx)
	pts, err := h.pointsRepo.List(ctx)
	if err != nil {
		return data, err
	}
	data.CanChoosePoint = st.Role != staff.RolePointStaff

	if !data.CanChoosePoint {
		if st.PointID == nil {
			data.NoPoint = t.T("admin.stock.no_point_assigned")
			return data, nil
		}
		for _, p := range pts {
			if p.ID == *st.PointID {
				data.PointID, data.PointName = p.ID, p.Name
			}
		}
		if data.PointID == "" {
			data.NoPoint = t.T("admin.stock.own_point_missing")
		}
		return data, nil
	}

	if len(pts) == 0 {
		data.NoPoint = t.T("admin.stock.no_points")
		return data, nil
	}
	chosen := ""
	for _, p := range pts {
		if p.ID == requested {
			chosen = p.ID
		}
	}
	if chosen == "" {
		chosen = pts[0].ID
		for _, p := range pts {
			if p.IsActive {
				chosen = p.ID
				break
			}
		}
	}
	for _, p := range pts {
		name := p.Name
		if !p.IsActive {
			name += " " + t.T("admin.common.inactive_suffix")
		}
		data.Points = append(data.Points, PointOptionVM{ID: p.ID, Name: name, Selected: p.ID == chosen})
		if p.ID == chosen {
			data.PointID, data.PointName = p.ID, p.Name
		}
	}
	return data, nil
}

// stockPage handles GET /admin/stock.
func (h *handlers) stockPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h.renderStockPage(w, r, q.Get("point"), strings.TrimSpace(q.Get("q")), parsePositiveInt(q.Get("page"), 1), q.Get("toast"), nil)
}

// stockOverlay carries a failed submission back into the re-rendered page.
type stockOverlay struct {
	err       string
	cells     map[string]StockCellVM // variant id -> the cell as submitted (changed or invalid cells only)
	conflicts map[string]bool        // variant id -> stock changed concurrently
}

func (h *handlers) renderStockPage(w http.ResponseWriter, r *http.Request, requestedPoint, query string, page int, toast string, overlay *stockOverlay) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)

	data, err := h.resolveStockPoint(ctx, st, requestedPoint)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}
	data.Query = query
	data.Page = page

	if data.PointID != "" {
		list, total, err := h.stockStore.List(ctx, data.PointID, query, page)
		if err != nil {
			h.renderInternalErr(w, err)
			return
		}
		for _, row := range list {
			data.Rows = append(data.Rows, buildStockPageRow(h.tr(r), row, data.PointID, data.PointName, st.Role != staff.RolePointStaff, overlay))
		}
		data.Empty = len(data.Rows) == 0
		data.CountLabel = h.tr(r).N(total, "admin.plural.variant")
		data.HasPrev = page > 1
		data.HasNext = total > page*stockPageSize
		data.PrevURL = stockPageURL(data.PointID, query, page-1, data.CanChoosePoint)
		data.NextURL = stockPageURL(data.PointID, query, page+1, data.CanChoosePoint)
	}
	if overlay != nil {
		data.Err = overlay.err
	}

	pageData := h.shellPageData("stock", "admin.nav.stock", st)
	pageData.Toast = toast
	pageData.Data = data
	if err := h.render.Render(w, "stock", pageData); err != nil {
		http.Error(w, h.tr(r).T("admin.err.render"), http.StatusInternalServerError)
	}
}

func buildStockPageRow(t tr, row stockPageRow, pointID, pointName string, canEditProduct bool, overlay *stockOverlay) StockPageRowVM {
	cell := StockCellVM{
		PointID:   pointID,
		PointName: pointName,
		Name:      stockFieldName(row.VariantID, pointID),
		OrigName:  stockOrigFieldName(row.VariantID, pointID),
		HasOrig:   true,
		Value:     strconv.Itoa(row.Quantity),
	}
	if row.HasStockRow {
		cell.Orig = cell.Value
	}
	if overlay != nil {
		if overlay.conflicts[row.VariantID] {
			cell.Conflict = true // Value/Orig above are already the fresh DB values
		} else if submitted, ok := overlay.cells[row.VariantID]; ok {
			cell.Value, cell.Orig, cell.Invalid = submitted.Value, submitted.Orig, submitted.Invalid
		}
	}

	vm := StockPageRowVM{
		VariantID:   row.VariantID,
		ProductName: row.ProductName,
		Brand:       row.Brand,
		Size:        row.Size,
		Color:       row.Color,
		Cell:        cell,
	}
	if canEditProduct {
		vm.EditURL = "/admin/products/" + row.ProductID
	}
	qty, _ := strconv.Atoi(cell.Value)
	vm.BadgeLbl, vm.BadgeFG, vm.BadgeBG = stockChip(t, qty)
	return vm
}

// stockSave handles POST /admin/stock: every row on the page submits
// variant_id + qty_<variant>_<point> + orig_<variant>_<point>; only changed
// cells are written (see parseStockCell / applyStockChangesTx).
func (h *handlers) stockSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)

	if err := r.ParseForm(); err != nil {
		http.Error(w, h.tr(r).T("admin.err.form"), http.StatusBadRequest)
		return
	}
	query := strings.TrimSpace(r.FormValue("q"))
	page := parsePositiveInt(r.FormValue("page"), 1)

	data, err := h.resolveStockPoint(ctx, st, r.FormValue("point"))
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}
	if data.PointID == "" {
		h.renderStockPage(w, r, "", query, page, "", &stockOverlay{err: data.NoPoint})
		return
	}
	pointID := data.PointID // point_staff: always their own point, never the posted one

	overlay := &stockOverlay{cells: map[string]StockCellVM{}, conflicts: map[string]bool{}}
	var changes []stockCellChange
	var errs []string
	for _, variantID := range r.Form["variant_id"] {
		name, origName := stockFieldName(variantID, pointID), stockOrigFieldName(variantID, pointID)
		raw, present := formValue(r.Form, name)
		origRaw, hasOrig := formValue(r.Form, origName)
		pc, err := parseStockCell(raw, present, origRaw, hasOrig)
		cell := StockCellVM{Value: strings.TrimSpace(raw), Orig: strings.TrimSpace(origRaw)}
		if err != nil {
			cell.Invalid = true
			overlay.cells[variantID] = cell
			errs = appendUnique(errs, errText(h.tr(r), err))
			continue
		}
		if pc.Changed {
			overlay.cells[variantID] = cell
			changes = append(changes, stockCellChange{RowKey: variantID, PointID: pointID, Qty: pc.Qty, Orig: pc.Orig})
		}
	}

	if len(errs) > 0 {
		overlay.err = h.tr(r).F("admin.stock.err_invalid_qty", strings.Join(errs, "; "))
		h.renderStockPage(w, r, pointID, query, page, "", overlay)
		return
	}

	if err := h.stockStore.Apply(ctx, changes); err != nil {
		var conflict *stockConflictError
		if errors.As(err, &conflict) {
			for _, c := range conflict.Cells {
				overlay.conflicts[c.RowKey] = true
			}
			overlay.err = h.tr(r).T(stockConflictMessage)
			h.renderStockPage(w, r, pointID, query, page, "", overlay)
			return
		}
		slog.ErrorContext(ctx, "admin stock save failed", "point_id", pointID, "err", err)
		overlay.err = appErrMessage(h.tr(r), err)
		h.renderStockPage(w, r, pointID, query, page, "", overlay)
		return
	}

	toast := h.tr(r).T("admin.stock.no_changes")
	if len(changes) > 0 {
		toast = h.tr(r).F("admin.stock.saved", h.tr(r).N(len(changes), "admin.plural.position"))
	}
	redirectWithToast(w, r, stockPageURL(pointID, query, page, data.CanChoosePoint), toast)
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
