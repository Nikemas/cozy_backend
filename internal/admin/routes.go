package admin

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/httpmw"
	"github.com/Nikemas/cozy_backend/internal/media"
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
// handlers.go's handlers struct for the full set. cfg builds the direct,
// non-presigned photo URLs (the same way internal/web's handlers.photoURL
// does); the photo upload itself is media.RegisterRoutes' POST
// /admin/api/media/upload, called from the product form's JS. mediaClient
// is the single *media.Client cmd/server/main.go already constructs for
// media.RegisterRoutes, not a second instance.
func RegisterRoutes(mux *http.ServeMux, db *sql.DB, staffSvc *staff.Service, mediaClient *media.Client, cfg *config.Config) error {
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
		orderMeta: newOrderListMetaRepo(db),
		customers: storefront.NewCustomerRepo(db),
		addresses: storefront.NewAddressRepo(db),

		categories: catalog.NewCategoryRepo(db),
		products:   catalog.NewProductRepo(db),
		variants:   catalog.NewVariantRepo(db),
		images:     catalog.NewImageRepo(db),
		stock:      catalog.NewStockRepo(db),
		media:      mediaClient,
		cfg:        cfg,

		productStore: newProductStore(db),
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
	mux.HandleFunc("GET /admin/reports", ownerOrManager(h.reportsPage))

	// Категории: list + add/edit modals + delete, same RBAC as products —
	// see categories_page.go. The JSON API at /admin/api/categories
	// (internal/httpapi/admin_catalog.go) already existed; this is the HTML
	// screen to drive it without a raw HTTP client.
	mux.HandleFunc("GET /admin/categories", ownerOrManager(h.categoriesPage))
	mux.HandleFunc("POST /admin/categories", ownerOrManager(h.categoriesCreate))
	mux.HandleFunc("POST /admin/categories/{id}", ownerOrManager(h.categoriesUpdate))
	mux.HandleFunc("POST /admin/categories/{id}/delete", ownerOrManager(h.categoriesDelete))

	// Task 5: points of sale + staff — both owner-only, list + add-modal +
	// toggle-active. See internal/admin/points_page.go/staff_page.go.
	mux.HandleFunc("GET /admin/points", ownerOnly(h.pointsPage))
	mux.HandleFunc("POST /admin/points", ownerOnly(h.pointsCreate))
	mux.HandleFunc("POST /admin/points/{id}/toggle", ownerOnly(h.pointsToggle))
	mux.HandleFunc("GET /admin/staff", ownerOnly(h.staffPage))
	mux.HandleFunc("POST /admin/staff", ownerOnly(h.staffCreate))
	mux.HandleFunc("POST /admin/staff/{id}/toggle", ownerOnly(h.staffToggle))

	// Task 2 (Товары): list, create/edit form, import — see products.go.
	// GET /admin/products/new and .../import are registered before the
	// {id} wildcard routes below, but Go 1.22's ServeMux already prefers a
	// literal segment over "{id}" at the same position regardless of
	// registration order, so this ordering is for readability only.
	mux.HandleFunc("GET /admin/products", ownerOrManager(h.productsListPage))
	mux.HandleFunc("GET /admin/products/new", ownerOrManager(h.productNewPage))
	mux.HandleFunc("POST /admin/products", ownerOrManager(h.productCreate))
	mux.HandleFunc("GET /admin/products/{id}", ownerOrManager(h.productEditPage))
	mux.HandleFunc("POST /admin/products/{id}", ownerOrManager(h.productUpdate))
	mux.HandleFunc("POST /admin/products/{id}/toggle-active", ownerOrManager(h.productToggleActive))
	// Удалить (per the design's canDelete) is owner-only, unlike every
	// other product write above — see productDelete's doc comment for why
	// it maps to the same soft-delete as "Деактивировать" under the hood.
	mux.HandleFunc("POST /admin/products/{id}/delete", ownerOnly(h.productDelete))

	// GET /admin/products/import renders the upload page; the actual
	// import POST already exists at this exact path/method
	// (httpapi.RegisterAdminImportRoutes, wired in cmd/server/main.go) —
	// see productImportPage's doc comment for why the page's own JS calls
	// that endpoint directly instead of this package registering a second
	// handler for the same pattern (net/http.ServeMux would panic on the
	// duplicate registration).
	mux.HandleFunc("GET /admin/products/import", ownerOrManager(h.productImportPage))

	// Same caching policy as the storefront's /static/ (internal/web's
	// staticMaxAge): short max-age + ETag, since URLs aren't hashed.
	mux.Handle("GET /admin/static/", http.StripPrefix("/admin/static/", httpmw.Static("admin/static", 10*time.Minute)))

	return nil
}
