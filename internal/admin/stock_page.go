// stock_page.go implements the "Остатки" screen (GET/POST /admin/stock):
// one point of sale's stock for every variant, editable in place.
//
// It is the point_staff view of the catalog from the admin brief (§3:
// "свои заказы + остатки своей точки ... только количество на складе") —
// a point_staff member (and a storekeeper, for its warehouse) is always
// pinned to their own staff.PointID and never sees a point selector;
// owner/manager pick any point. Saving uses the same optimistic,
// changed-cells-only write as the product form (product_store.go), in one
// transaction. For a warehouse every row also has a storage-location
// select (stock.location_id), saved by the same form — only when changed.
//
// The SQL lives in stock_page_repo.go.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// --- view models ---

// PointOptionVM is one <option> of a point-of-sale select.
type PointOptionVM struct {
	ID       string
	Name     string
	Selected bool
}

// OptionVM is one <option> of a filter or location select.
type OptionVM struct {
	Value    string
	Label    string
	Selected bool
}

// StockLocCellVM is a warehouse row's storage-location select.
type StockLocCellVM struct {
	Name     string // loc_<variant>_<point>
	OrigName string // orig_loc_<variant>_<point>
	Value    string // location id as shown ("" = none)
	Orig     string // location id the row was rendered with
	Options  []OptionVM
}

// StockPageRowVM is one row of the Остатки table.
type StockPageRowVM struct {
	VariantID   string
	ProductName string
	ModelCode   string
	Brand       string
	Size        string
	Color       string
	EditURL     string // product form link, "" for point_staff / storekeeper
	Cell        StockCellVM
	Loc         *StockLocCellVM // nil unless the point is a warehouse
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
	IsWarehouse    bool
	NoPoint        string // why no point can be shown ("" when one is selected)

	Query      string
	CategoryID string
	Color      string
	LocationID string
	Categories []OptionVM
	Colors     []OptionVM
	Locations  []OptionVM // «Место» filter, warehouses only
	HasFilters bool
	ResetURL   string

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

func stockLocFieldName(key, pointID string) string     { return "loc_" + key + "_" + pointID }
func stockLocOrigFieldName(key, pointID string) string { return "orig_loc_" + key + "_" + pointID }

func stockPageURL(f stockFilter, canChoosePoint bool) string {
	v := url.Values{}
	if canChoosePoint && f.PointID != "" {
		v.Set("point", f.PointID)
	}
	for k, val := range map[string]string{"q": f.Query, "cat": f.CategoryID, "color": f.Color, "loc": f.LocationID} {
		if val != "" {
			v.Set(k, val)
		}
	}
	if f.Page > 1 {
		v.Set("page", strconv.Itoa(f.Page))
	}
	if len(v) == 0 {
		return "/admin/stock"
	}
	return "/admin/stock?" + v.Encode()
}

// stockFilterFrom reads the filter from a query string or a posted form.
func stockFilterFrom(get func(string) string) stockFilter {
	return stockFilter{
		Query:      strings.TrimSpace(get("q")),
		CategoryID: strings.TrimSpace(get("cat")),
		Color:      strings.TrimSpace(get("color")),
		LocationID: strings.TrimSpace(get("loc")),
		Page:       parsePositiveInt(get("page"), 1),
	}
}

// resolveStockPoint picks the point the page shows: point_staff and a
// storekeeper are pinned to their own point (whatever ?point= says);
// owner/manager get the requested point if it exists, else the first
// active one.
func (h *handlers) resolveStockPoint(ctx context.Context, st *staff.Staff, requested string) (data StockPageData, err error) {
	t := trFromContext(ctx)
	pts, err := h.pointsRepo.List(ctx)
	if err != nil {
		return data, err
	}
	data.CanChoosePoint = !st.Role.PinnedToPoint()

	if !data.CanChoosePoint {
		if st.PointID == nil {
			data.NoPoint = t.T("admin.stock.no_point_assigned")
			return data, nil
		}
		for _, p := range pts {
			if p.ID == *st.PointID {
				data.PointID, data.PointName, data.IsWarehouse = p.ID, p.Name, p.Kind == points.KindWarehouse
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
	chosen := pickStockPoint(pts, requested)
	for _, p := range pts {
		name := p.Name
		if p.Kind == points.KindWarehouse {
			name += " · " + t.T("admin.product.warehouse_suffix")
		}
		if !p.IsActive {
			name += " " + t.T("admin.common.inactive_suffix")
		}
		data.Points = append(data.Points, PointOptionVM{ID: p.ID, Name: name, Selected: p.ID == chosen})
		if p.ID == chosen {
			data.PointID, data.PointName, data.IsWarehouse = p.ID, p.Name, p.Kind == points.KindWarehouse
		}
	}
	return data, nil
}

// pickStockPoint returns requested when it exists, else the first active
// point, else the first one.
func pickStockPoint(pts []*points.Point, requested string) string {
	for _, p := range pts {
		if p.ID == requested {
			return p.ID
		}
	}
	for _, p := range pts {
		if p.IsActive {
			return p.ID
		}
	}
	return pts[0].ID
}

// stockPage handles GET /admin/stock.
func (h *handlers) stockPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := stockFilterFrom(q.Get)
	f.PointID = q.Get("point")
	h.renderStockPage(w, r, f, h.pageToast(r), nil)
}

// stockOverlay carries a failed submission back into the re-rendered page.
type stockOverlay struct {
	err       string
	cells     map[string]StockCellVM // variant id -> the cell as submitted (changed or invalid cells only)
	locs      map[string]string      // variant id -> the location as submitted
	conflicts map[string]bool        // variant id -> stock changed concurrently
}

func (h *handlers) renderStockPage(w http.ResponseWriter, r *http.Request, f stockFilter, toast string, overlay *stockOverlay) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)
	t := h.tr(r)

	data, err := h.resolveStockPoint(ctx, st, f.PointID)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}
	if !data.IsWarehouse {
		f.LocationID = ""
	}
	f.PointID = data.PointID
	data.Query, data.CategoryID, data.Color, data.LocationID, data.Page = f.Query, f.CategoryID, f.Color, f.LocationID, f.Page
	data.HasFilters = f.Query != "" || f.CategoryID != "" || f.Color != "" || f.LocationID != ""
	data.ResetURL = stockPageURL(stockFilter{PointID: data.PointID}, data.CanChoosePoint)

	if data.PointID != "" {
		if err := h.fillStockPage(ctx, t, st, &data, f, overlay); err != nil {
			h.renderInternalErr(w, err)
			return
		}
	}
	if overlay != nil {
		data.Err = overlay.err
	}

	pageData := h.shellPageData("stock", "admin.nav.stock", st)
	pageData.Toast = toast
	pageData.Data = data
	if err := h.render.Render(w, "stock", pageData); err != nil {
		http.Error(w, t.T("admin.err.render"), http.StatusInternalServerError)
	}
}

// fillStockPage loads the filter options and the rows of the chosen point.
func (h *handlers) fillStockPage(ctx context.Context, t tr, st *staff.Staff, data *StockPageData, f stockFilter, overlay *stockOverlay) error {
	cats, err := h.stockStore.Categories(ctx)
	if err != nil {
		return err
	}
	data.Categories = stockCategoryOptions(t, cats, f.CategoryID)
	colors, err := h.stockStore.Colors(ctx)
	if err != nil {
		return err
	}
	for _, c := range colors {
		data.Colors = append(data.Colors, OptionVM{Value: c, Label: c, Selected: strings.EqualFold(c, f.Color)})
	}
	var locs []stockLocation
	if data.IsWarehouse {
		if locs, err = h.stockStore.Locations(ctx, data.PointID); err != nil {
			return err
		}
		data.Locations = stockLocationFilterOptions(t, locs, f.LocationID)
	}

	list, total, err := h.stockStore.List(ctx, f)
	if err != nil {
		return err
	}
	canEditProduct := !st.Role.PinnedToPoint()
	for _, row := range list {
		vm := buildStockPageRow(t, row, data.PointID, data.PointName, canEditProduct, overlay)
		if data.IsWarehouse {
			vm.Loc = buildStockLocCell(t, row, data.PointID, locs, overlay)
		}
		data.Rows = append(data.Rows, vm)
	}
	data.Empty = len(data.Rows) == 0
	data.CountLabel = t.N(total, "admin.plural.variant")
	data.HasPrev = f.Page > 1
	data.HasNext = total > f.Page*stockPageSize
	prev, next := f, f
	prev.Page, next.Page = f.Page-1, f.Page+1
	data.PrevURL = stockPageURL(prev, data.CanChoosePoint)
	data.NextURL = stockPageURL(next, data.CanChoosePoint)
	return nil
}

// stockCategoryOptions is the category filter: top-level categories with
// their subcategories indented below them (any depth).
func stockCategoryOptions(t tr, cats []stockCategory, selected string) []OptionVM {
	children := map[string][]stockCategory{}
	for _, c := range cats {
		children[c.ParentID] = append(children[c.ParentID], c)
	}
	var out []OptionVM
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		if depth > 5 { // guards against a parent cycle in bad data
			return
		}
		for _, c := range children[parent] {
			name := c.NameRu
			if t.Lang() == i18n.LangKY && c.NameKy != "" {
				name = c.NameKy
			}
			out = append(out, OptionVM{Value: c.ID, Label: strings.Repeat("— ", depth) + name, Selected: c.ID == selected})
			walk(c.ID, depth+1)
		}
	}
	walk("", 0)
	return out
}

func stockLocationFilterOptions(t tr, locs []stockLocation, selected string) []OptionVM {
	out := []OptionVM{{Value: stockLocationNone, Label: t.T("admin.stock.no_location"), Selected: selected == stockLocationNone}}
	for _, l := range locs {
		out = append(out, OptionVM{Value: l.ID, Label: locationLabel(t, l), Selected: l.ID == selected})
	}
	return out
}

func locationLabel(t tr, l stockLocation) string {
	if l.IsActive {
		return l.Name
	}
	return l.Name + " " + t.T("admin.common.inactive_suffix")
}

// buildStockLocCell is a warehouse row's location select: "no location",
// every active location, plus the current one even if it was deactivated.
func buildStockLocCell(t tr, row stockPageRow, pointID string, locs []stockLocation, overlay *stockOverlay) *StockLocCellVM {
	cell := &StockLocCellVM{
		Name:     stockLocFieldName(row.VariantID, pointID),
		OrigName: stockLocOrigFieldName(row.VariantID, pointID),
		Value:    row.LocationID,
		Orig:     row.LocationID,
	}
	if overlay != nil && !overlay.conflicts[row.VariantID] {
		if v, ok := overlay.locs[row.VariantID]; ok {
			cell.Value = v
		}
	}
	cell.Options = []OptionVM{{Value: "", Label: t.T("admin.stock.no_location"), Selected: cell.Value == ""}}
	for _, l := range locs {
		if l.IsActive || l.ID == cell.Value || l.ID == cell.Orig {
			cell.Options = append(cell.Options, OptionVM{Value: l.ID, Label: locationLabel(t, l), Selected: l.ID == cell.Value})
		}
	}
	return cell
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
		ModelCode:   row.ModelCode,
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

// parseLocationCell reads one location select (loc_…) against its
// rendered original (orig_loc_…). changed is false when the select wasn't
// submitted or still holds the original; loc nil means "no location".
func parseLocationCell(form url.Values, name, origName string) (loc *string, changed bool) {
	raw, present := formValue(form, name)
	if !present {
		return nil, false
	}
	raw = strings.TrimSpace(raw)
	orig, hasOrig := formValue(form, origName)
	if strings.TrimSpace(orig) == raw && (hasOrig || raw == "") {
		return nil, false
	}
	if raw == "" {
		return nil, true
	}
	return &raw, true
}

// stockSave handles POST /admin/stock: every row on the page submits
// variant_id + qty_<variant>_<point> + orig_<variant>_<point> (and, for a
// warehouse, loc_/orig_loc_); only changed cells are written (see
// parseStockCell / applyStockChangesTx / applyStockLocationsTx).
func (h *handlers) stockSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)

	if err := r.ParseForm(); err != nil {
		http.Error(w, h.tr(r).T("admin.err.form"), http.StatusBadRequest)
		return
	}
	f := stockFilterFrom(r.FormValue)

	data, err := h.resolveStockPoint(ctx, st, r.FormValue("point"))
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}
	if data.PointID == "" {
		h.renderStockPage(w, r, f, "", &stockOverlay{err: data.NoPoint})
		return
	}
	f.PointID = data.PointID // point_staff / storekeeper: always their own point, never the posted one

	overlay, changes, locChanges, errs := parseStockSubmission(h.tr(r), r.Form, data.PointID, data.IsWarehouse)
	if len(errs) > 0 {
		overlay.err = h.tr(r).F("admin.stock.err_invalid_qty", strings.Join(errs, "; "))
		h.renderStockPage(w, r, f, "", overlay)
		return
	}

	if err := h.stockStore.Apply(ctx, changes, locChanges); err != nil {
		var conflict *stockConflictError
		if errors.As(err, &conflict) {
			for _, c := range conflict.Cells {
				overlay.conflicts[c.RowKey] = true
			}
			overlay.err = h.tr(r).T(stockConflictMessage)
			h.renderStockPage(w, r, f, "", overlay)
			return
		}
		slog.ErrorContext(ctx, "admin stock save failed", "point_id", f.PointID, "err", err)
		overlay.err = appErrMessage(h.tr(r), err)
		h.renderStockPage(w, r, f, "", overlay)
		return
	}

	toast := toastKey("stock_no_changes")
	if n := countChangedRows(changes, locChanges); n > 0 {
		toast = toastCount("stock_saved", n)
	}
	redirectWithToast(w, r, stockPageURL(f, data.CanChoosePoint), toast)
}

// parseStockSubmission reads every row of the posted page for pointID.
func parseStockSubmission(t tr, form url.Values, pointID string, isWarehouse bool) (*stockOverlay, []stockCellChange, []stockLocationChange, []string) {
	overlay := &stockOverlay{cells: map[string]StockCellVM{}, locs: map[string]string{}, conflicts: map[string]bool{}}
	var changes []stockCellChange
	var locChanges []stockLocationChange
	var errs []string
	for _, variantID := range form["variant_id"] {
		if isWarehouse {
			if loc, changed := parseLocationCell(form, stockLocFieldName(variantID, pointID), stockLocOrigFieldName(variantID, pointID)); changed {
				locChanges = append(locChanges, stockLocationChange{RowKey: variantID, PointID: pointID, LocationID: loc})
				overlay.locs[variantID] = deref(loc)
			}
		}
		name, origName := stockFieldName(variantID, pointID), stockOrigFieldName(variantID, pointID)
		raw, present := formValue(form, name)
		origRaw, hasOrig := formValue(form, origName)
		pc, err := parseStockCell(raw, present, origRaw, hasOrig)
		cell := StockCellVM{Value: strings.TrimSpace(raw), Orig: strings.TrimSpace(origRaw)}
		if err != nil {
			cell.Invalid = true
			overlay.cells[variantID] = cell
			errs = appendUnique(errs, errText(t, err))
			continue
		}
		if pc.Changed {
			overlay.cells[variantID] = cell
			changes = append(changes, stockCellChange{RowKey: variantID, PointID: pointID, Qty: pc.Qty, Orig: pc.Orig})
		}
	}
	return overlay, changes, locChanges, errs
}

// countChangedRows is the number of distinct rows touched by a save.
func countChangedRows(changes []stockCellChange, locs []stockLocationChange) int {
	seen := map[string]bool{}
	for _, c := range changes {
		seen[c.RowKey] = true
	}
	for _, c := range locs {
		seen[c.RowKey] = true
	}
	return len(seen)
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
