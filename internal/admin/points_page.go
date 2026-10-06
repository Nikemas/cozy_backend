// points_page.go implements Task 5's "Точки продаж" screen (GET
// /admin/points) plus its mutations (POST /admin/points to create, POST
// /admin/points/{id} to edit name/address, POST /admin/points/{id}/toggle
// to flip active/inactive) — design canvas
// Cozy Admin.dc.html lines 620-642 (list) and 684-703 (pointModal). All
// three routes are wired under ownerOnly in routes.go.
package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// pointRow is one row of the points list, pre-computed server-side so
// points.gohtml only ranges and prints — no chip-color/label logic in the
// template itself.
type pointRow struct {
	ID          string
	Name        string
	City        string
	Address     string
	FullAddress string // "city, address" shown in the list
	Hours       string
	Lat, Lng    string // formatted for the edit form; empty when unset
	IsActive    bool
	ChipLabel   string
	ChipClass   string // admin-chip modifier class, see admin.css
	ToggleLabel string
}

// newPointRow derives the two-state active/inactive chip + toggle-button
// label design canvas's {{ p.chipStyle }}/{{ p.chipLabel }}/{{ p.toggle }}
// bindings computed dynamically there. Colors: active reuses the same
// green as in-stock/delivered order-status chips (#2E7D32/#E8F5E9);
// inactive reuses the neutral grey of the "placed" order-status chip
// (#8A8A86/#EDEDEB) — see admin.css's .admin-chip--active/--inactive.
func newPointRow(t tr, p *points.Point) pointRow {
	row := pointRow{
		ID: p.ID, Name: p.Name, City: p.City, Address: p.Address, Hours: p.WorkingHours, IsActive: p.IsActive,
		FullAddress: strings.Trim(p.City+", "+p.Address, ", "),
		Lat:         formatCoord(p.Latitude), Lng: formatCoord(p.Longitude),
	}
	if p.IsActive {
		row.ChipLabel = t.T("admin.points.active")
		row.ChipClass = "admin-chip--active"
		row.ToggleLabel = t.T("admin.common.deactivate")
	} else {
		row.ChipLabel = t.T("admin.points.inactive")
		row.ChipClass = "admin-chip--inactive"
		row.ToggleLabel = t.T("admin.common.activate")
	}
	return row
}

// pointsPageData is points.gohtml's PageData.Data payload.
type pointsPageData struct {
	Rows  []pointRow
	Error string // set when a create/toggle POST fails; empty otherwise
}

// pointsPage handles GET /admin/points.
func (h *handlers) pointsPage(w http.ResponseWriter, r *http.Request) {
	h.renderPointsPage(w, r, "")
}

// renderPointsPage re-lists every point of sale and renders the points
// screen, optionally with errMsg surfaced as a banner — used both by the
// plain GET and by the create/toggle handlers below when their mutation
// fails, so the failure is shown on the page instead of a silent no-op or
// a generic 500.
func (h *handlers) renderPointsPage(w http.ResponseWriter, r *http.Request, errMsg string) {
	st, _ := staff.FromContext(r.Context())

	list, err := h.pointsRepo.List(r.Context())
	if err != nil {
		http.Error(w, h.tr(r).T("admin.points.load_failed"), http.StatusInternalServerError)
		return
	}
	rows := make([]pointRow, 0, len(list))
	for _, p := range list {
		rows = append(rows, newPointRow(h.tr(r), p))
	}

	data := h.shellPageData("points", "admin.nav.points", st)
	data.Data = pointsPageData{Rows: rows, Error: errMsg}
	data.FormRetry = withRetryError(pointsFormRetry(r, h.tr(r)), errMsg)
	data.Toast = h.pageToast(r)
	if err := h.render.Render(w, "points", data); err != nil {
		http.Error(w, h.tr(r).T("admin.err.render"), http.StatusInternalServerError)
	}
}

// pointsCreate handles POST /admin/points — the pointModal form (name, city,
// address, hours, coordinates). New points are created active by default (the design's
// pointModal has no active/inactive toggle of its own). On success it
// redirects back to /admin/points (POST-redirect-GET, avoids a resubmit on
// refresh); on failure (validation) it re-renders the list with the error
// message instead of losing the user's input to a blank error page.
func (h *handlers) pointsCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderPointsPage(w, r, h.tr(r).T("admin.err.form"))
		return
	}

	in, err := pointInputFromForm(r)
	if err != nil {
		h.renderPointsPage(w, r, h.tr(r).T("admin.points.invalid_coords"))
		return
	}
	in.IsActive = true
	created, err := h.pointsRepo.Create(r.Context(), in)
	if err != nil {
		h.renderPointsPage(w, r, appErrMessage(h.tr(r), err))
		return
	}
	h.auditPoint(r.Context(), audit.ActionPointCreate, created.ID, nil, in)

	redirectWithToast(w, r, "/admin/points", toastKey("point_saved"))
}

// pointsUpdate handles POST /admin/points/{id} — the edit modal (name, city,
// address, hours, coordinates). The active flag is left as it is; that's the toggle's job.
func (h *handlers) pointsUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		h.renderPointsPage(w, r, h.tr(r).T("admin.err.form"))
		return
	}

	current, err := h.pointsRepo.GetByID(r.Context(), id)
	if err != nil {
		h.renderPointsPage(w, r, appErrMessage(h.tr(r), err))
		return
	}

	in, err := pointInputFromForm(r)
	if err != nil {
		h.renderPointsPage(w, r, h.tr(r).T("admin.points.invalid_coords"))
		return
	}
	in.IsActive = current.IsActive
	if _, err := h.pointsRepo.Update(r.Context(), id, in); err != nil {
		h.renderPointsPage(w, r, appErrMessage(h.tr(r), err))
		return
	}
	h.auditPoint(r.Context(), audit.ActionPointUpdate, id, current, in)

	redirectWithToast(w, r, "/admin/points", toastKey("point_saved"))
}

// pointsToggle handles POST /admin/points/{id}/toggle — the row's "toggle
// active" button (deactivation goes through a confirm dialog, see
// points.gohtml). Re-submits the point's own Name/Address unchanged with
// IsActive flipped via PointsRepo.Update.
func (h *handlers) pointsToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	target, err := h.pointsRepo.GetByID(r.Context(), id)
	if err != nil {
		h.renderPointsPage(w, r, appErrMessage(h.tr(r), err))
		return
	}

	in := points.PointInput{
		Name: target.Name, City: target.City, Address: target.Address, WorkingHours: target.WorkingHours,
		Latitude: target.Latitude, Longitude: target.Longitude, IsActive: !target.IsActive,
	}
	if _, err := h.pointsRepo.Update(r.Context(), id, in); err != nil {
		h.renderPointsPage(w, r, appErrMessage(h.tr(r), err))
		return
	}
	toggleAction := audit.ActionPointDeactivate
	if in.IsActive {
		toggleAction = audit.ActionPointActivate
	}
	h.auditPoint(r.Context(), toggleAction, id, target, in)

	toast := toastKey("point_deactivated")
	if in.IsActive {
		toast = toastKey("point_activated")
	}
	redirectWithToast(w, r, "/admin/points", toast)
}

func formatCoord(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

// parseCoord reads an optional coordinate from a form field; "42,87" and
// "42.87" are both accepted. Blank means "not set".
func parseCoord(raw string) (*float64, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, ",", "."))
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// pointInputFromForm reads the point modal's fields. IsActive is set by the
// caller (create: true, edit: unchanged).
func pointInputFromForm(r *http.Request) (points.PointInput, error) {
	lat, err := parseCoord(r.FormValue("latitude"))
	if err != nil {
		return points.PointInput{}, err
	}
	lng, err := parseCoord(r.FormValue("longitude"))
	if err != nil {
		return points.PointInput{}, err
	}
	return points.PointInput{
		Name:         strings.TrimSpace(r.FormValue("name")),
		City:         strings.TrimSpace(r.FormValue("city")),
		Address:      strings.TrimSpace(r.FormValue("address")),
		WorkingHours: strings.TrimSpace(r.FormValue("working_hours")),
		Latitude:     lat,
		Longitude:    lng,
	}, nil
}
