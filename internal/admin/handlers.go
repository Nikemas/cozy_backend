package admin

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/media"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// handlers holds everything the admin panel's HTTP handlers need. Task
// 2-5's own handler files (same package) add fields here (repos, other
// services) as needed rather than inventing a second struct — see
// RegisterRoutes for how h is built and threaded through.
type handlers struct {
	staffSvc   *staff.Service
	render     *Renderer
	reports    reportsBackend
	pointsRepo *points.PointsRepo // Task 5 (points + staff screens), also used by Task 2's product form and Task 3's order detail

	// Task 3 (Заказы, internal/admin/orders.go): the order-management
	// service plus the small set of other Wave 3 repos its detail page
	// enriches an order with (customer phone, delivery address) — see
	// orders.go's adminOrdersService for exactly which *orders.Service
	// methods are used.
	ordersSvc adminOrdersService
	orderMeta orderListMeta // batch item counts + phones for the list page
	customers *storefront.CustomerRepo
	addresses *storefront.AddressRepo

	// Task 2 (products: list/form/import) — see products.go.
	categories *catalog.CategoryRepo
	products   *catalog.ProductRepo
	variants   *catalog.VariantRepo
	images     *catalog.ImageRepo
	stock      *catalog.StockRepo
	media      *media.Client
	cfg        *config.Config

	// fix/admin (B5): transactional product-form save (product_store.go)
	// and the per-point Остатки screen (stock_page.go).
	productStore productSaver
	stockStore   stockPageStore
}

// loginPage renders GET /admin/login. A staff member who already has a
// valid session skips straight past it to the first page their role can
// see, instead of being shown a login form again.
func (h *handlers) loginPage(w http.ResponseWriter, r *http.Request) {
	if st, err := h.staffSvc.StaffFromRequest(r); err == nil && st != nil {
		http.Redirect(w, r, firstAllowedPath(st.Role), http.StatusSeeOther)
		return
	}

	h.renderLogin(w, r, PageData{Phone: r.URL.Query().Get("phone")})
}

// loginSubmit handles POST /admin/login: calls the existing
// staff.Service.Login (unchanged by this task), sets the staff_session
// cookie on success with the same attributes the JSON login handler uses
// (staff.SetSessionCookie), and redirects to the first admin page this
// staff member's role can see. On failure it re-renders the same form with
// Err set and Phone kept sticky, mirroring the design canvas's err state.
func (h *handlers) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderLogin(w, r, PageData{Err: h.tr(r).T("admin.err.form")})
		return
	}
	phone := r.FormValue("phone")
	password := r.FormValue("password")

	token, err := h.staffSvc.Login(r.Context(), phone, password)
	if err != nil {
		// appErrMessage shows only the user-facing message of an
		// *apperr.AppError (wrong credentials, too many attempts) and a
		// generic text for anything else — a raw err.Error() could leak
		// SQL/driver details onto an unauthenticated page.
		var appErr *apperr.AppError
		if !errors.As(err, &appErr) {
			slog.ErrorContext(r.Context(), "admin: staff login failed", "err", err)
		}
		h.renderLogin(w, r, PageData{Phone: phone, Err: appErrMessage(h.tr(r), err)})
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

// renderLogin fills in Lang/Screen/PageTitle/ShowSidebar for the login
// screen so every call site only has to set Phone/Err. The login page is
// outside the auth-gate, so its language comes from r directly.
func (h *handlers) renderLogin(w http.ResponseWriter, r *http.Request, data PageData) {
	data.Lang = langFromRequest(r)
	data.Screen = "login"
	data.PageTitle = "admin.login.title"
	data.ShowSidebar = false
	if err := h.render.Render(w, "login", data); err != nil {
		http.Error(w, h.tr(r).T("admin.err.render"), http.StatusInternalServerError)
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
		PageTitle:   "admin.no_access.title",
		ShowSidebar: false,
		Staff:       st,
	}
	if err := h.render.Render(w, "no_access", data); err != nil {
		http.Error(w, h.tr(r).T("admin.err.render"), http.StatusInternalServerError)
	}
}

// shellPageData builds the PageData common to every screen rendered
// inside the full app shell (sidebar + header) — nav items, initials, and
// role label all derive from st, which the auth-gate has already
// guaranteed is non-nil by the time a stub/real page handler runs.
//
// title may be a locale key ("admin.nav.orders") or ready text — the
// templates print it through {{t}} either way.
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
