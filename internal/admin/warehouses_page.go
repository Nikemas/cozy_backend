// warehouses_page.go implements the «Склады» section (feat/warehouses):
// warehouses (points_of_sale.kind = 'warehouse') are listed and managed
// here instead of under «Точки продаж», and each warehouse has its own
// page with its storage locations (места хранения, storage_locations).
//
// RBAC: the owner manages every warehouse. A storekeeper sees only its own
// warehouse (staff.point_id) and may only manage that warehouse's storage
// locations — creating/renaming/deactivating warehouses stays owner-only
// (routes.go). Everyone else never reaches these handlers.
package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

const warehousesPath = "/admin/warehouses"

func warehouseURL(id string) string { return warehousesPath + "/" + id }

// registerWarehouseRoutes mounts «Склады». ownerOnly guards the warehouse
// rows themselves; ownerOrStorekeeper the pages and the storage locations,
// whose handlers additionally pin a storekeeper to its own warehouse.
func registerWarehouseRoutes(mux *http.ServeMux, h *handlers, ownerOnly, ownerOrStorekeeper func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET "+warehousesPath, ownerOrStorekeeper(h.warehousesPage))
	mux.HandleFunc("POST "+warehousesPath, ownerOnly(h.warehousesCreate))
	mux.HandleFunc("GET "+warehousesPath+"/{id}", ownerOrStorekeeper(h.warehouseDetailPage))
	mux.HandleFunc("POST "+warehousesPath+"/{id}", ownerOnly(h.warehousesUpdate))
	mux.HandleFunc("POST "+warehousesPath+"/{id}/toggle", ownerOnly(h.warehousesToggle))
	mux.HandleFunc("POST "+warehousesPath+"/{id}/locations", ownerOrStorekeeper(h.locationCreate))
	mux.HandleFunc("POST "+warehousesPath+"/{id}/locations/{loc}", ownerOrStorekeeper(h.locationUpdate))
	mux.HandleFunc("POST "+warehousesPath+"/{id}/locations/{loc}/toggle", ownerOrStorekeeper(h.locationToggle))
	mux.HandleFunc("POST "+warehousesPath+"/{id}/locations/{loc}/delete", ownerOrStorekeeper(h.locationDelete))
}

// warehouseRow is one row of the warehouses list.
type warehouseRow struct {
	pointRow
	URL            string
	LocationsLabel string // "3 места хранения"
}

// warehousesPageData backs warehouses.gohtml.
type warehousesPageData struct {
	Rows  []warehouseRow
	Error string
}

// warehousesPage handles GET /admin/warehouses. A storekeeper has exactly
// one warehouse, so it goes straight to that warehouse's page.
func (h *handlers) warehousesPage(w http.ResponseWriter, r *http.Request) {
	st, _ := staff.FromContext(r.Context())
	if st.Role == staff.RoleStorekeeper {
		if st.PointID == nil {
			h.renderWarehousesPage(w, r, h.tr(r).T("admin.stock.no_point_assigned"))
			return
		}
		http.Redirect(w, r, warehouseURL(*st.PointID), http.StatusSeeOther)
		return
	}
	h.renderWarehousesPage(w, r, "")
}

func (h *handlers) renderWarehousesPage(w http.ResponseWriter, r *http.Request, errMsg string) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)
	t := h.tr(r)

	var rows []warehouseRow
	if st.Role == staff.RoleOwner {
		list, err := h.pointsRepo.List(ctx)
		if err != nil {
			http.Error(w, t.T("admin.points.load_failed"), http.StatusInternalServerError)
			return
		}
		locs, err := h.locations.ListAll(ctx)
		if err != nil {
			h.renderInternalErr(w, err)
			return
		}
		_, warehouses := splitPointsByKind(list)
		for _, p := range warehouses {
			rows = append(rows, warehouseRow{
				pointRow:       newWarehouseRow(t, p),
				URL:            warehouseURL(p.ID),
				LocationsLabel: t.N(len(locs[p.ID]), "admin.plural.location"),
			})
		}
	}

	data := h.shellPageData("warehouses", "admin.nav.warehouses", st)
	data.Data = warehousesPageData{Rows: rows, Error: errMsg}
	data.FormRetry = withRetryError(warehousesFormRetry(r, t), errMsg)
	data.Toast = h.pageToast(r)
	if err := h.render.Render(w, "warehouses", data); err != nil {
		http.Error(w, t.T("admin.err.render"), http.StatusInternalServerError)
	}
}

// newWarehouseRow is newPointRow with the warehouse wording of the chip
// and toggle (a warehouse is never shown to customers either way).
func newWarehouseRow(t tr, p *points.Point) pointRow {
	row := newPointRow(t, p)
	if p.IsActive {
		row.ChipLabel = t.T("admin.warehouses.active")
	} else {
		row.ChipLabel = t.T("admin.warehouses.inactive")
	}
	return row
}

// warehousesFormRetry reopens the add/edit modal after a failed POST.
func warehousesFormRetry(r *http.Request, t tr) *FormRetry {
	if !isFormRetryRequest(r) {
		return nil
	}
	fields := []string{"name", "city", "address"}
	switch {
	case r.URL.Path == warehousesPath:
		return newFormRetry(r, "admin-warehouse-modal", r.URL.Path, t.T("admin.warehouses.new_title"), fields...)
	case strings.HasPrefix(r.URL.Path, warehousesPath+"/") && strings.Count(r.URL.Path, "/") == 3:
		return newFormRetry(r, "admin-warehouse-modal", r.URL.Path, t.T("admin.warehouses.edit_title"), fields...)
	default:
		return nil
	}
}

// warehouseInputFromForm reads the warehouse modal: name, city, address.
// Working hours and coordinates only matter for customer-facing branches.
func warehouseInputFromForm(r *http.Request) points.PointInput {
	return points.PointInput{
		Name:    strings.TrimSpace(r.FormValue("name")),
		City:    strings.TrimSpace(r.FormValue("city")),
		Address: strings.TrimSpace(r.FormValue("address")),
		Kind:    points.KindWarehouse,
	}
}

// warehousesCreate handles POST /admin/warehouses (owner).
func (h *handlers) warehousesCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderWarehousesPage(w, r, h.tr(r).T("admin.err.form"))
		return
	}
	in := warehouseInputFromForm(r)
	in.IsActive = true
	created, err := h.pointsRepo.Create(r.Context(), in)
	if err != nil {
		h.renderWarehousesPage(w, r, appErrMessage(h.tr(r), err))
		return
	}
	h.auditPoint(r.Context(), audit.ActionPointCreate, created.ID, nil, in)
	redirectWithToast(w, r, warehouseURL(created.ID), toastKey("warehouse_saved"))
}

// warehousesUpdate handles POST /admin/warehouses/{id} (owner): name,
// city, address; everything else stays as it is.
func (h *handlers) warehousesUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		h.renderWarehousesPage(w, r, h.tr(r).T("admin.err.form"))
		return
	}
	current, err := h.pointOfKind(r.Context(), id, points.KindWarehouse)
	if err != nil {
		h.renderWarehousesPage(w, r, appErrMessage(h.tr(r), err))
		return
	}
	in := warehouseInputFromForm(r)
	in.WorkingHours, in.Latitude, in.Longitude, in.IsActive = current.WorkingHours, current.Latitude, current.Longitude, current.IsActive
	if _, err := h.pointsRepo.Update(r.Context(), id, in); err != nil {
		h.renderWarehousesPage(w, r, appErrMessage(h.tr(r), err))
		return
	}
	h.auditPoint(r.Context(), audit.ActionPointUpdate, id, current, in)
	back := warehousesPath
	if r.FormValue("back") == warehouseURL(id) { // edited from the warehouse's own page
		back = warehouseURL(id)
	}
	redirectWithToast(w, r, back, toastKey("warehouse_saved"))
}

// warehousesToggle handles POST /admin/warehouses/{id}/toggle (owner).
func (h *handlers) warehousesToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	target, err := h.pointOfKind(r.Context(), id, points.KindWarehouse)
	if err != nil {
		h.renderWarehousesPage(w, r, appErrMessage(h.tr(r), err))
		return
	}
	in := points.PointInput{
		Name: target.Name, City: target.City, Address: target.Address, WorkingHours: target.WorkingHours,
		Latitude: target.Latitude, Longitude: target.Longitude, Kind: target.Kind, IsActive: !target.IsActive,
	}
	if _, err := h.pointsRepo.Update(r.Context(), id, in); err != nil {
		h.renderWarehousesPage(w, r, appErrMessage(h.tr(r), err))
		return
	}
	action, toast := audit.ActionPointDeactivate, "warehouse_deactivated"
	if in.IsActive {
		action, toast = audit.ActionPointActivate, "warehouse_activated"
	}
	h.auditPoint(r.Context(), action, id, target, in)
	redirectWithToast(w, r, warehousesPath, toastKey(toast))
}

// --- one warehouse: its storage locations ---

// locationRow is one storage location on the warehouse page.
type locationRow struct {
	ID          string
	Name        string
	SortOrder   int
	IsActive    bool
	HasStock    bool // goods lie here: can't be deleted, only deactivated
	StockLabel  string
	ChipLabel   string
	ChipClass   string
	ToggleLabel string
}

func newLocationRow(t tr, l *points.Location) locationRow {
	row := locationRow{
		ID: l.ID, Name: l.Name, SortOrder: l.SortOrder, IsActive: l.IsActive,
		HasStock: l.StockQty > 0, StockLabel: t.F("admin.stock.pcs", l.StockQty),
	}
	if l.IsActive {
		row.ChipLabel, row.ChipClass, row.ToggleLabel = t.T("admin.points.active"), "admin-chip--active", t.T("admin.common.deactivate")
	} else {
		row.ChipLabel, row.ChipClass, row.ToggleLabel = t.T("admin.points.inactive"), "admin-chip--inactive", t.T("admin.common.activate")
	}
	return row
}

// warehouseDetailData backs warehouse_detail.gohtml.
type warehouseDetailData struct {
	Warehouse pointRow
	URL       string
	Locations []locationRow
	// CanEditWarehouse: the owner may rename/deactivate the warehouse
	// itself; a storekeeper only manages its locations.
	CanEditWarehouse bool
	StockURL         string
	Error            string
}

// warehouseForStaff loads warehouse id for st: a storekeeper only ever
// gets its own warehouse — any other id reads as not found.
func (h *handlers) warehouseForStaff(ctx context.Context, st *staff.Staff, id string) (*points.Point, error) {
	if st.Role == staff.RoleStorekeeper && (st.PointID == nil || *st.PointID != id) {
		return nil, apperrPointNotFound()
	}
	return h.pointOfKind(ctx, id, points.KindWarehouse)
}

// warehouseDetailPage handles GET /admin/warehouses/{id}.
func (h *handlers) warehouseDetailPage(w http.ResponseWriter, r *http.Request) {
	h.renderWarehouseDetail(w, r, "")
}

func (h *handlers) renderWarehouseDetail(w http.ResponseWriter, r *http.Request, errMsg string) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)
	t := h.tr(r)

	wh, err := h.warehouseForStaff(ctx, st, r.PathValue("id"))
	if err != nil {
		redirectWithToast(w, r, h.warehousesHome(st), toastForErr(err))
		return
	}
	list, err := h.locations.ListByPoint(ctx, wh.ID)
	if err != nil {
		h.renderInternalErr(w, err)
		return
	}
	rows := make([]locationRow, 0, len(list))
	for _, l := range list {
		rows = append(rows, newLocationRow(t, l))
	}

	data := h.shellPageData("warehouses", "admin.nav.warehouses", st)
	if st.Role == staff.RoleOwner {
		data.ShowBack, data.BackURL = true, warehousesPath
	}
	data.Data = warehouseDetailData{
		Warehouse: newWarehouseRow(t, wh), URL: warehouseURL(wh.ID), Locations: rows,
		CanEditWarehouse: st.Role == staff.RoleOwner,
		StockURL:         stockPageURL(stockFilter{PointID: wh.ID}, st.Role == staff.RoleOwner),
		Error:            errMsg,
	}
	data.FormRetry = withRetryError(locationFormRetry(r), errMsg)
	data.Toast = h.pageToast(r)
	if err := h.render.Render(w, "warehouse_detail", data); err != nil {
		http.Error(w, t.T("admin.err.render"), http.StatusInternalServerError)
	}
}

// warehousesHome is where a failed lookup sends st back to.
func (h *handlers) warehousesHome(st *staff.Staff) string {
	if st.Role == staff.RoleStorekeeper {
		return "/admin/stock"
	}
	return warehousesPath
}

// locationFormRetry reopens the edit-location modal after a failed rename
// (the add form is inline and keeps its value via the same mechanism).
func locationFormRetry(r *http.Request) *FormRetry {
	if !isFormRetryRequest(r) || !strings.Contains(r.URL.Path, "/locations/") || strings.Count(r.URL.Path, "/") != 5 {
		return nil
	}
	return newFormRetry(r, "admin-location-modal", r.URL.Path, "", "name", "sort_order")
}

// locationInputFromForm reads name + optional sort order.
func locationInputFromForm(r *http.Request) points.LocationInput {
	in := points.LocationInput{Name: r.FormValue("name")}
	if n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("sort_order"))); err == nil && n > 0 {
		in.SortOrder = n
	}
	return in
}

// locationAction runs one storage-location mutation of the warehouse in
// the URL (pinned for a storekeeper) and redirects back to its page with
// toast, or re-renders the page with the error.
func (h *handlers) locationAction(w http.ResponseWriter, r *http.Request, toast string, do func(ctx context.Context, warehouseID string) error) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)
	if err := r.ParseForm(); err != nil {
		h.renderWarehouseDetail(w, r, h.tr(r).T("admin.err.form"))
		return
	}
	wh, err := h.warehouseForStaff(ctx, st, r.PathValue("id"))
	if err != nil {
		redirectWithToast(w, r, h.warehousesHome(st), toastForErr(err))
		return
	}
	if err := do(ctx, wh.ID); err != nil {
		h.renderWarehouseDetail(w, r, appErrMessage(h.tr(r), err))
		return
	}
	redirectWithToast(w, r, warehouseURL(wh.ID), toastKey(toast))
}

// locationCreate handles POST /admin/warehouses/{id}/locations.
func (h *handlers) locationCreate(w http.ResponseWriter, r *http.Request) {
	h.locationAction(w, r, "location_saved", func(ctx context.Context, whID string) error {
		_, err := h.locations.Create(ctx, whID, locationInputFromForm(r))
		return err
	})
}

// locationUpdate handles POST /admin/warehouses/{id}/locations/{loc}.
func (h *handlers) locationUpdate(w http.ResponseWriter, r *http.Request) {
	h.locationAction(w, r, "location_saved", func(ctx context.Context, whID string) error {
		_, err := h.locations.Update(ctx, whID, r.PathValue("loc"), locationInputFromForm(r))
		return err
	})
}

// locationToggle handles POST .../locations/{loc}/toggle.
func (h *handlers) locationToggle(w http.ResponseWriter, r *http.Request) {
	toast := "location_deactivated"
	if r.FormValue("active") == "1" {
		toast = "location_activated"
	}
	h.locationAction(w, r, toast, func(ctx context.Context, whID string) error {
		return h.locations.SetActive(ctx, whID, r.PathValue("loc"), r.FormValue("active") == "1")
	})
}

// locationDelete handles POST .../locations/{loc}/delete: refused while
// goods lie at the location (location_in_use) — deactivate it instead.
func (h *handlers) locationDelete(w http.ResponseWriter, r *http.Request) {
	h.locationAction(w, r, "location_deleted", func(ctx context.Context, whID string) error {
		return h.locations.Delete(ctx, whID, r.PathValue("loc"))
	})
}
