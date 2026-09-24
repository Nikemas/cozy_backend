// points_page.go implements Task 5's "Точки продаж" screen (GET
// /admin/points) plus its mutations (POST /admin/points to create, POST
// /admin/points/{id} to edit name/address, POST /admin/points/{id}/toggle
// to flip active/inactive) — design canvas
// Cozy Admin.dc.html lines 620-642 (list) and 684-703 (pointModal). All
// three routes are wired under ownerOnly in routes.go.
package admin

import (
	"net/http"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// pointRow is one row of the points list, pre-computed server-side so
// points.gohtml only ranges and prints — no chip-color/label logic in the
// template itself.
type pointRow struct {
	ID          string
	Name        string
	Address     string
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
func newPointRow(p *points.Point) pointRow {
	row := pointRow{ID: p.ID, Name: p.Name, Address: p.Address, IsActive: p.IsActive}
	if p.IsActive {
		row.ChipLabel = "Активна"
		row.ChipClass = "admin-chip--active"
		row.ToggleLabel = "Деактивировать"
	} else {
		row.ChipLabel = "Неактивна"
		row.ChipClass = "admin-chip--inactive"
		row.ToggleLabel = "Активировать"
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
		http.Error(w, "не удалось загрузить точки продаж", http.StatusInternalServerError)
		return
	}
	rows := make([]pointRow, 0, len(list))
	for _, p := range list {
		rows = append(rows, newPointRow(p))
	}

	data := h.shellPageData("points", "Склад и точки", st)
	data.Data = pointsPageData{Rows: rows, Error: errMsg}
	if err := h.render.Render(w, "points", data); err != nil {
		http.Error(w, "ошибка рендеринга страницы", http.StatusInternalServerError)
	}
}

// pointsCreate handles POST /admin/points — the pointModal form (name +
// address). New points are created active by default (the design's
// pointModal has no active/inactive toggle of its own). On success it
// redirects back to /admin/points (POST-redirect-GET, avoids a resubmit on
// refresh); on failure (validation) it re-renders the list with the error
// message instead of losing the user's input to a blank error page.
func (h *handlers) pointsCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderPointsPage(w, r, "не удалось прочитать форму")
		return
	}

	in := points.PointInput{
		Name:     strings.TrimSpace(r.FormValue("name")),
		Address:  strings.TrimSpace(r.FormValue("address")),
		IsActive: true,
	}
	if _, err := h.pointsRepo.Create(r.Context(), in); err != nil {
		h.renderPointsPage(w, r, appErrMessage(err))
		return
	}

	http.Redirect(w, r, "/admin/points", http.StatusSeeOther)
}

// pointsUpdate handles POST /admin/points/{id} — the edit modal (name +
// address). The active flag is left as it is; that's the toggle's job.
func (h *handlers) pointsUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		h.renderPointsPage(w, r, "не удалось прочитать форму")
		return
	}

	current, err := h.pointsRepo.GetByID(r.Context(), id)
	if err != nil {
		h.renderPointsPage(w, r, appErrMessage(err))
		return
	}

	in := points.PointInput{
		Name:     strings.TrimSpace(r.FormValue("name")),
		Address:  strings.TrimSpace(r.FormValue("address")),
		IsActive: current.IsActive,
	}
	if _, err := h.pointsRepo.Update(r.Context(), id, in); err != nil {
		h.renderPointsPage(w, r, appErrMessage(err))
		return
	}

	http.Redirect(w, r, "/admin/points", http.StatusSeeOther)
}

// pointsToggle handles POST /admin/points/{id}/toggle — the row's "toggle
// active" button (deactivation goes through a confirm dialog, see
// points.gohtml). Re-submits the point's own Name/Address unchanged with
// IsActive flipped via PointsRepo.Update.
func (h *handlers) pointsToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	target, err := h.pointsRepo.GetByID(r.Context(), id)
	if err != nil {
		h.renderPointsPage(w, r, appErrMessage(err))
		return
	}

	in := points.PointInput{Name: target.Name, Address: target.Address, IsActive: !target.IsActive}
	if _, err := h.pointsRepo.Update(r.Context(), id, in); err != nil {
		h.renderPointsPage(w, r, appErrMessage(err))
		return
	}

	http.Redirect(w, r, "/admin/points", http.StatusSeeOther)
}
