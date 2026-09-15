package admin

import (
	"database/sql"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// RegisterRoutes mounts the admin HTML/HTMX panel on mux under /admin/*
// (the login/logout HTML flow plus one page per screen) — separate from
// /admin/api/* (internal/staff, internal/points, internal/httpapi's
// admin_*.go, all Wave 3), which this package's pages call into via HTMX
// for anything beyond Foundation's stub screens.
//
// db backs each screen's own repos/services as Tasks 2-5 land — see
// handlers.go's handlers struct for the full set.
func RegisterRoutes(mux *http.ServeMux, db *sql.DB, staffSvc *staff.Service) error {
	renderer, err := NewRenderer()
	if err != nil {
		return err
	}

	h := &handlers{
		staffSvc: staffSvc,
		render:   renderer,

		reports:    newReportsRepo(db),
		pointsRepo: points.NewPointsRepo(db),

		ordersSvc: orders.NewService(db),
		customers: storefront.NewCustomerRepo(db),
		addresses: storefront.NewAddressRepo(db),
	}

	mux.HandleFunc("GET /admin/login", h.loginPage)
	mux.HandleFunc("POST /admin/login", h.loginSubmit)
	mux.HandleFunc("POST /admin/logout", h.logoutSubmit)

	// /admin/no-access only requires being logged in as SOME staff member
	// — it's the landing page for a role (point_staff, this wave) that
	// isn't allowed on any of the screens below, not a page with its own
	// stricter role requirement.
	anyRole := requireStaffRole(staffSvc, staff.RoleOwner, staff.RoleManager, staff.RolePointStaff)
	mux.HandleFunc("GET /admin/no-access", anyRole(h.noAccessPage))

	ownerOrManager := requireStaffRole(staffSvc, staff.RoleOwner, staff.RoleManager)
	ownerOnly := requireStaffRole(staffSvc, staff.RoleOwner)

	// The 5 real screens + /admin/products/import, all stubbed for now
	// per Wave 4 Task 1's acceptance criteria ("RegisterRoutes регистрирует
	// все 6 путей ... сразу, но с заглушечными хендлерами") — Tasks 2-5
	// replace stubPage(...) with their real handlers, not these routes.
	mux.HandleFunc("GET /admin/orders", ownerOrManager(h.ordersListPage))
	mux.HandleFunc("GET /admin/orders/{id}", ownerOrManager(h.orderDetailPage))
	mux.HandleFunc("POST /admin/orders/{id}/status", ownerOrManager(h.orderStatusUpdate))
	mux.HandleFunc("GET /admin/products", ownerOrManager(h.stubPage("products", "Товары")))
	mux.HandleFunc("GET /admin/products/import", ownerOrManager(h.stubPage("products", "Импорт товаров")))
	mux.HandleFunc("GET /admin/reports", ownerOrManager(h.reportsPage))

	// Task 5: points of sale + staff — both owner-only, list + add-modal +
	// toggle-active. See internal/admin/points_page.go/staff_page.go.
	mux.HandleFunc("GET /admin/points", ownerOnly(h.pointsPage))
	mux.HandleFunc("POST /admin/points", ownerOnly(h.pointsCreate))
	mux.HandleFunc("POST /admin/points/{id}/toggle", ownerOnly(h.pointsToggle))
	mux.HandleFunc("GET /admin/staff", ownerOnly(h.staffPage))
	mux.HandleFunc("POST /admin/staff", ownerOnly(h.staffCreate))
	mux.HandleFunc("POST /admin/staff/{id}/toggle", ownerOnly(h.staffToggle))

	mux.Handle("GET /admin/static/", http.StripPrefix("/admin/static/", http.FileServer(http.Dir("admin/static"))))

	return nil
}
