// staff_page.go implements Task 5's "Сотрудники" screen (GET /admin/staff)
// plus its mutations (POST /admin/staff to create, POST
// /admin/staff/{id}/toggle to flip active/inactive, POST
// /admin/staff/{id}/password for the owner to set a new password) — design
// canvas Cozy Admin.dc.html lines 644-664 (list) and 706-741 (staffModal).
// All routes are wired under ownerOnly in routes.go.
package admin

import (
	"net/http"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// staffRow is one row of the staff list, pre-computed server-side so
// staff.gohtml only ranges and prints.
type staffRow struct {
	ID          string
	Initials    string
	Name        string
	Phone       string
	RoleLabel   string
	RoleClass   string // admin-chip modifier class, see admin.css
	Scope       string // "Все точки" for owner/manager, the point's name for point_staff
	ToggleLabel string
	IsActive    bool // deactivation asks for confirmation, activation doesn't
}

// roleChipClass picks a role-chip color, design canvas's {{ s.chipStyle }}
// computed dynamically there — not specified by the canvas (it only models
// owner/manager in its data, and doesn't style them distinctly), so this
// task's own choice: owner reuses the primary tint (it's the highest
// role, same visual weight as the "+ Добавить" buttons), manager reuses
// the same blue as the "confirmed" order-status chip, and point_staff a
// neutral grey (same family as the inactive/placed chips).
func roleChipClass(role staff.Role) string {
	switch role {
	case staff.RoleOwner:
		return "admin-chip--role-owner"
	case staff.RoleManager:
		return "admin-chip--role-manager"
	default:
		return "admin-chip--role-point-staff"
	}
}

// newStaffRow builds one row, resolving PointID to a human-readable point
// name via pointNames (built once per request in renderStaffPage, keyed
// by points.Point.ID) rather than showing the raw UUID.
func newStaffRow(s staff.Staff, pointNames map[string]string) staffRow {
	scope := "Все точки"
	if s.Role == staff.RolePointStaff {
		scope = "точка не найдена"
		if s.PointID != nil {
			if name, ok := pointNames[*s.PointID]; ok {
				scope = name
			}
		}
	}

	toggleLabel := "Деактивировать"
	if !s.IsActive {
		toggleLabel = "Активировать"
	}

	return staffRow{
		ID:          s.ID,
		Initials:    initialsFor(s.Name),
		Name:        s.Name,
		Phone:       s.Phone,
		RoleLabel:   roleLabel(s.Role),
		RoleClass:   roleChipClass(s.Role),
		Scope:       scope,
		ToggleLabel: toggleLabel,
		IsActive:    s.IsActive,
	}
}

// staffPageData is staff.gohtml's PageData.Data payload. Points backs the
// add-staff modal's point-of-sale select (shown only for role=point_staff,
// see staff.gohtml's toggleStaffPointField()) — the design canvas's
// staffModal has no such field at all, added here because
// staff.Service.CreateStaff requires point_id for point_staff and rejects
// it for owner/manager (§ Business rules 1).
type staffPageData struct {
	Rows   []staffRow
	Points []*points.Point
	Error  string
	Notice string
	// MinPasswordLength drives the password inputs' minlength/placeholder
	// so the form hint can't drift from the server-side rule.
	MinPasswordLength int
}

// staffNotices are the success banners a redirect back to the list can ask
// for via ?done=... — a fixed map, so the query string can't inject text.
var staffNotices = map[string]string{
	"password": "Пароль изменён. Сотрудник выйдет из всех сеансов и войдёт с новым паролем.",
}

// staffPage handles GET /admin/staff.
func (h *handlers) staffPage(w http.ResponseWriter, r *http.Request) {
	h.renderStaffPageWithNotice(w, r, "", staffNotices[r.URL.Query().Get("done")])
}

// renderStaffPage re-lists every staff account and every point of sale
// (for the modal's point select) and renders the staff screen, optionally
// with errMsg surfaced as a banner — used both by the plain GET and by the
// create/toggle handlers below when their mutation fails. In particular
// this is how the last-owner-invariant conflict from
// staff.Service.UpdateStaff reaches the page instead of being swallowed.
func (h *handlers) renderStaffPage(w http.ResponseWriter, r *http.Request, errMsg string) {
	h.renderStaffPageWithNotice(w, r, errMsg, "")
}

func (h *handlers) renderStaffPageWithNotice(w http.ResponseWriter, r *http.Request, errMsg, notice string) {
	st, _ := staff.FromContext(r.Context())

	list, err := h.staffSvc.ListStaff(r.Context())
	if err != nil {
		http.Error(w, "не удалось загрузить сотрудников", http.StatusInternalServerError)
		return
	}
	pts, err := h.pointsRepo.List(r.Context())
	if err != nil {
		http.Error(w, "не удалось загрузить точки продаж", http.StatusInternalServerError)
		return
	}

	pointNames := make(map[string]string, len(pts))
	for _, p := range pts {
		pointNames[p.ID] = p.Name
	}

	rows := make([]staffRow, 0, len(list))
	for _, s := range list {
		rows = append(rows, newStaffRow(s, pointNames))
	}

	data := h.shellPageData("staff", "Сотрудники", st)
	data.Data = staffPageData{Rows: rows, Points: pts, Error: errMsg, Notice: notice, MinPasswordLength: staff.MinPasswordLength}
	if err := h.render.Render(w, "staff", data); err != nil {
		http.Error(w, "ошибка рендеринга страницы", http.StatusInternalServerError)
	}
}

// staffCreate handles POST /admin/staff — the staffModal form (name,
// phone, role, optional point, password). point_id is only read (and
// only required) when role is point_staff — validateStaffRolePointID
// (internal/staff/service.go) rejects the opposite combination just as
// strictly. On success it redirects back to /admin/staff; on failure
// (duplicate phone, missing point_id, etc.) it re-renders the list with
// the error message.
func (h *handlers) staffCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderStaffPage(w, r, "не удалось прочитать форму")
		return
	}

	role := staff.Role(r.FormValue("role"))
	var pointID *string
	if role == staff.RolePointStaff {
		if v := strings.TrimSpace(r.FormValue("point_id")); v != "" {
			pointID = &v
		}
	}

	in := staff.CreateStaffInput{
		Phone:    strings.TrimSpace(r.FormValue("phone")),
		Password: r.FormValue("password"),
		Name:     strings.TrimSpace(r.FormValue("name")),
		Role:     role,
		PointID:  pointID,
	}
	created, err := h.staffSvc.CreateStaff(r.Context(), in)
	if err != nil {
		h.renderStaffPage(w, r, appErrMessage(err))
		return
	}
	h.auditStaff(r.Context(), audit.ActionStaffCreate, created.ID, created.Name,
		map[string]any{"phone": created.Phone, "role": string(created.Role), "point_id": deref(created.PointID)})

	http.Redirect(w, r, "/admin/staff", http.StatusSeeOther)
}

// staffToggle handles POST /admin/staff/{id}/toggle — the row's "toggle
// active" button. Service has no dedicated toggle method, so this looks
// the account up (via ListStaff, already needed to render the page) and
// resubmits its own Name/Role/PointID unchanged with IsActive flipped via
// UpdateStaff, leaving the password untouched (Password: nil). If this
// would demote/deactivate the sole active owner, UpdateStaff's repo layer
// rejects it with apperr.Conflict("last_owner", ...); that message is
// surfaced on the page via appErrMessage/renderStaffPage rather than
// silently failing or producing a generic 500.
func (h *handlers) staffToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	list, err := h.staffSvc.ListStaff(r.Context())
	if err != nil {
		http.Error(w, "не удалось загрузить сотрудников", http.StatusInternalServerError)
		return
	}
	var target *staff.Staff
	for i := range list {
		if list[i].ID == id {
			target = &list[i]
			break
		}
	}
	if target == nil {
		h.renderStaffPage(w, r, "сотрудник не найден")
		return
	}

	in := staff.UpdateStaffInput{
		Name:     target.Name,
		Role:     target.Role,
		PointID:  target.PointID,
		IsActive: !target.IsActive,
	}
	if _, err := h.staffSvc.UpdateStaff(r.Context(), id, in); err != nil {
		h.renderStaffPage(w, r, appErrMessage(err))
		return
	}
	staffAction := audit.ActionStaffDeactivate
	if in.IsActive {
		staffAction = audit.ActionStaffActivate
	}
	h.auditStaff(r.Context(), staffAction, id, target.Name, map[string]any{"is_active": audit.Change{From: target.IsActive, To: in.IsActive}})

	http.Redirect(w, r, "/admin/staff", http.StatusSeeOther)
}

// staffResetPassword handles POST /admin/staff/{id}/password — the owner
// sets a new password for a staff member (who forgot theirs, or whose
// password may have leaked). staff.Service.ResetPassword enforces the
// minimum length and ends all of that account's sessions.
func (h *handlers) staffResetPassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderStaffPage(w, r, "не удалось прочитать форму")
		return
	}
	password := r.FormValue("password")
	if password != r.FormValue("password_confirm") {
		h.renderStaffPage(w, r, "пароли не совпадают")
		return
	}
	if err := h.staffSvc.ResetPassword(r.Context(), r.PathValue("id"), password); err != nil {
		h.renderStaffPage(w, r, appErrMessage(err))
		return
	}
	h.auditStaff(r.Context(), audit.ActionStaffPassword, r.PathValue("id"), "", nil)
	http.Redirect(w, r, "/admin/staff?done=password", http.StatusSeeOther)
}
