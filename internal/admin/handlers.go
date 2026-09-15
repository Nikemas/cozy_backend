package admin

import (
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// handlers holds everything the admin panel's HTTP handlers need. Task
// 2-5's own handler files (same package) add fields here (repos, other
// services) as needed rather than inventing a second struct — see
// RegisterRoutes for how h is built and threaded through.
type handlers struct {
	staffSvc *staff.Service
	render   *Renderer
	reports  reportsBackend
}

// loginPage renders GET /admin/login. A staff member who already has a
// valid session skips straight past it to the first page their role can
// see, instead of being shown a login form again.
func (h *handlers) loginPage(w http.ResponseWriter, r *http.Request) {
	if st, err := h.staffSvc.StaffFromRequest(r); err == nil && st != nil {
		http.Redirect(w, r, firstAllowedPath(st.Role), http.StatusSeeOther)
		return
	}

	h.renderLogin(w, PageData{Phone: r.URL.Query().Get("phone")})
}

// loginSubmit handles POST /admin/login: calls the existing
// staff.Service.Login (unchanged by this task), sets the staff_session
// cookie on success with the same attributes the JSON login handler uses
// (staff.SetSessionCookie), and redirects to the first admin page this
// staff member's role can see. On failure it re-renders the same form with
// Err set and Phone kept sticky, mirroring the design canvas's err state.
func (h *handlers) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderLogin(w, PageData{Err: "не удалось прочитать форму"})
		return
	}
	phone := r.FormValue("phone")
	password := r.FormValue("password")

	token, err := h.staffSvc.Login(r.Context(), phone, password)
	if err != nil {
		h.renderLogin(w, PageData{Phone: phone, Err: err.Error()})
		return
	}

	// Login only returns the raw session token, not the staff member
	// (its signature is unchanged by this task) — resolve the role for
	// the post-login redirect by looking the fresh token up the same way
	// any other request would, via a request carrying it as a cookie.
	st, err := h.staffForToken(r, token)
	if err != nil || st == nil {
		// Should not happen right after a successful Login, but fail
		// safe (send back to the login page) rather than dereference a
		// nil *staff.Staff below.
		staff.SetSessionCookie(w, token, staff.SessionCookieMaxAge)
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	staff.SetSessionCookie(w, token, staff.SessionCookieMaxAge)
	http.Redirect(w, r, firstAllowedPath(st.Role), http.StatusSeeOther)
}

// staffForToken resolves the staff member a freshly issued session token
// belongs to, without yet having set it as a cookie on the real response —
// staff.Service.StaffFromRequest only knows how to read the cookie off an
// *http.Request, so this builds one carrying just that cookie.
func (h *handlers) staffForToken(r *http.Request, token string) (*staff.Staff, error) {
	cloned := r.Clone(r.Context())
	cloned.Header.Del("Cookie")
	cloned.AddCookie(&http.Cookie{Name: staff.SessionCookieName, Value: token})
	return h.staffSvc.StaffFromRequest(cloned)
}

// renderLogin fills in Screen/PageTitle/ShowSidebar for the login screen
// so every call site only has to set Phone/Err.
func (h *handlers) renderLogin(w http.ResponseWriter, data PageData) {
	data.Screen = "login"
	data.PageTitle = "Вход"
	data.ShowSidebar = false
	if err := h.render.Render(w, "login", data); err != nil {
		http.Error(w, "ошибка рендеринга страницы", http.StatusInternalServerError)
	}
}

// logoutSubmit handles POST /admin/logout: revokes the session (idempotent
// — an unknown/already-revoked token is a no-op, same as the JSON API) and
// always clears the cookie and redirects to the login page, even if
// Logout itself errors, so a database hiccup here doesn't strand the
// browser holding a cookie it can no longer use.
func (h *handlers) logoutSubmit(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(staff.SessionCookieName); err == nil && cookie.Value != "" {
		_ = h.staffSvc.Logout(r.Context(), cookie.Value)
	}
	staff.SetSessionCookie(w, "", -1)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// noAccessPage handles GET /admin/no-access — landing page for a staff
// member whose role (point_staff, this wave) can't see any admin screen.
func (h *handlers) noAccessPage(w http.ResponseWriter, r *http.Request) {
	st, _ := staff.FromContext(r.Context())
	data := PageData{
		Screen:      "no_access",
		PageTitle:   "Нет доступа",
		ShowSidebar: false,
		Staff:       st,
	}
	if err := h.render.Render(w, "no_access", data); err != nil {
		http.Error(w, "ошибка рендеринга страницы", http.StatusInternalServerError)
	}
}

// stubPage returns a handler that renders screen inside the full app
// shell with just a title — Task 2-5 replace these with real content
// without touching RegisterRoutes' wiring (auth-gate, layout, nav).
func (h *handlers) stubPage(screen, title string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, _ := staff.FromContext(r.Context())
		data := h.shellPageData(screen, title, st)
		if err := h.render.Render(w, screen, data); err != nil {
			http.Error(w, "ошибка рендеринга страницы", http.StatusInternalServerError)
		}
	}
}

// shellPageData builds the PageData common to every screen rendered
// inside the full app shell (sidebar + header) — nav items, initials, and
// role label all derive from st, which the auth-gate has already
// guaranteed is non-nil by the time a stub/real page handler runs.
func (h *handlers) shellPageData(screen, title string, st *staff.Staff) PageData {
	return PageData{
		Screen:      screen,
		PageTitle:   title,
		ShowSidebar: true,
		Staff:       st,
		Initials:    initialsFor(st.Name),
		RoleLabel:   roleLabel(st.Role),
		NavItems:    navItemsForRole(st.Role, screen),
	}
}
