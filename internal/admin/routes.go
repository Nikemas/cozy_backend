package admin

import (
	"database/sql"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/banner"
	"github.com/Nikemas/cozy_backend/internal/broadcasts"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/httpmw"
	"github.com/Nikemas/cozy_backend/internal/media"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/reports"
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
// media.RegisterRoutes, not a second instance. reportsRepo is likewise the
// single reports.CachedRepo also given to httpapi.RegisterAdminReportsRoutes,
// so the reports page and the export share one cache.
func RegisterRoutes(mux *http.ServeMux, db *sql.DB, staffSvc *staff.Service, mediaClient *media.Client, cfg *config.Config, reportsRepo *reports.CachedRepo) error {
	renderer, err := NewRenderer()
	if err != nil {
		return err
	}
	auditLog := audit.New(db)

	h := &handlers{
		staffSvc: staffSvc,
		render:   renderer,

		reports:    reportsRepo,
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
		// feat/category-photo: the Категории screen's photo uploads.
		categoryImages: mediaClient,
		cfg:            cfg,

		productStore: &productStore{db: db, audit: auditLog},
		stockStore:   &stockPageRepo{db: db, audit: auditLog},

		importer:       newSQLProductImporter(db),
		importTokenKey: newImportTokenKey(),
		importLimiter:  newImportLimiter(),

		audit:      auditLog,
		auditList:  auditLog,
		productOps: &productOpsStore{db: db, audit: auditLog},
	}

	mux.HandleFunc("GET /admin/login", h.loginPage)
	mux.HandleFunc("POST /admin/login", h.loginSubmit)
	mux.HandleFunc("POST /admin/logout", h.logoutSubmit)
	// RU/KY switcher (header + login page) — see lang.go.
	mux.HandleFunc("POST /admin/lang", h.setLang)

	// /admin/no-access only requires being logged in as SOME staff member
	// — it's the landing page for a role that isn't allowed on any of the
	// screens below, not a page with its own stricter role requirement.
	// fix/admin-owner-ux: every staff page also counts the viewer's
	// unprocessed orders for the sidebar badge (withNavBadges, GET only).
	badges := withNavBadges(orderBadgeRepo{db: db})
	anyRole := chainMiddleware(requireStaffRole(staffSvc, staff.RoleOwner, staff.RoleManager, staff.RolePointStaff), badges)
	mux.HandleFunc("GET /admin/no-access", anyRole(h.noAccessPage))

	ownerOrManager := chainMiddleware(requireStaffRole(staffSvc, staff.RoleOwner, staff.RoleManager), badges)
	ownerOnly := chainMiddleware(requireStaffRole(staffSvc, staff.RoleOwner), badges)

	// The 5 real screens + /admin/products/import, all stubbed for now
	// per Wave 4 Task 1's acceptance criteria ("RegisterRoutes регистрирует
	// все 6 путей ... сразу, но с заглушечными хендлерами") — Tasks 2-5
	// replace stubPage(...) with their real handlers, not these routes.
	// Orders are open to point_staff too, scoped to their own point inside
	// the handlers (same rule as the JSON API in httpapi/admin_orders.go).
	mux.HandleFunc("GET /admin/orders", anyRole(h.ordersListPage))
	// fix/admin-ux-followups: the sidebar badge polls this every 60s.
	mux.HandleFunc("GET /admin/orders/badge", anyRole(h.ordersBadge))
	mux.HandleFunc("GET /admin/orders/{id}", anyRole(h.orderDetailPage))
	mux.HandleFunc("POST /admin/orders/{id}/status", anyRole(h.orderStatusUpdate))
	// fix/admin-ops: bulk status change from the list (same RBAC per order).
	mux.HandleFunc("POST /admin/orders/bulk-status", anyRole(h.orderBulkStatus))

	// Остатки: one point's stock, editable. point_staff is pinned to its
	// own point — see stock_page.go.
	mux.HandleFunc("GET /admin/stock", anyRole(h.stockPage))
	mux.HandleFunc("POST /admin/stock", anyRole(h.stockSave))
	mux.HandleFunc("GET /admin/reports", ownerOrManager(h.reportsPage))

	// W2 fix/promo-push: Рассылки (promo push) — broadcasts_page.go; the
	// sending itself is broadcasts.Worker, started in cmd/server.
	registerBroadcastRoutes(mux, h, broadcasts.NewRepo(db), ownerOrManager)
	// feat/home-banner: Баннер на главной (site home + app home screen) —
	// banner_page.go; same roles as Категории/Рассылки.
	registerBannerRoutes(mux, &bannerPages{
		h: h, store: banner.NewStore(db), images: mediaClient,
		categoryTree: h.categories.Tree, objectURL: cfg.PublicObjectURL,
	}, ownerOrManager)

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
	mux.HandleFunc("POST /admin/points/{id}", ownerOnly(h.pointsUpdate))
	mux.HandleFunc("POST /admin/points/{id}/toggle", ownerOnly(h.pointsToggle))
	mux.HandleFunc("GET /admin/staff", ownerOnly(h.staffPage))
	mux.HandleFunc("POST /admin/staff", ownerOnly(h.staffCreate))
	mux.HandleFunc("POST /admin/staff/{id}/toggle", ownerOnly(h.staffToggle))
	mux.HandleFunc("POST /admin/staff/{id}/password", ownerOnly(h.staffResetPassword))

	// Доставка: delivery zones (fee, free-from threshold) — delivery.go.
	registerDeliveryRoutes(mux, h, orders.NewDeliveryZoneRepo(db), ownerOnly)
	// fix/admin-ops: audit journal (owner only) — see audit_page.go.
	mux.HandleFunc("GET /admin/audit", ownerOnly(h.auditPage))

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
	// fix/admin-ops: bulk activate/deactivate/move-to-category.
	mux.HandleFunc("POST /admin/products/bulk", ownerOrManager(h.productsBulk))
	// Удалить (per the design's canDelete) is owner-only, unlike every
	// other product write above — see productDelete's doc comment for why
	// it maps to the same soft-delete as "Деактивировать" under the hood.
	mux.HandleFunc("POST /admin/products/{id}/delete", ownerOnly(h.productDelete))

	// Импорт (product_import.go): the page plus its htmx check/apply/reset.
	// POST /admin/products/import itself is the JSON endpoint
	// (httpapi.RegisterAdminImportRoutes), kept for API clients.
	mux.HandleFunc("GET "+importBasePath, ownerOrManager(h.productImportPage))
	mux.HandleFunc("POST "+importCheckPath, ownerOrManager(h.productImportCheck))
	mux.HandleFunc("POST "+importApplyPath, ownerOrManager(h.productImportApply))
	mux.HandleFunc("GET "+importResetPath, ownerOrManager(h.productImportReset))

	// Same caching policy as the storefront's /static/: a year for URLs
	// carrying the file's current ?v= hash (templates use {{asset}}),
	// short max-age + ETag otherwise.
	mux.Handle("GET "+staticURLPrefix, http.StripPrefix(staticURLPrefix, httpmw.VersionedStatic(staticDir, renderer.assets, staticMaxAge)))

	return nil
}
