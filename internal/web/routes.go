// Package web renders the public storefront (cozy.kg): html/template
// pages under web/templates, static assets under web/static, an httpOnly
// session cookie layered over internal/auth's existing OTP flow, and the
// routing skeleton for all 9 screens from COZY_WEB_DESIGN.md. Task 1
// (Foundation) wires the chrome and placeholder content; Tasks 2-5 fill
// each screen in without needing to touch routing or the session
// middleware set up here.
package web

import (
	"database/sql"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// RegisterRoutes mounts the storefront on mux. authSvc must be the same
// *auth.Service instance registerAPIRoutes uses for /api/v1/auth/* — the
// web login flow calls its RequestOTP/VerifyOTP directly rather than
// duplicating the OTP business logic.
func RegisterRoutes(mux *http.ServeMux, db *sql.DB, cfg *config.Config, authSvc *auth.Service) error {
	bundle, err := i18n.Load("locales")
	if err != nil {
		return err
	}
	renderer, err := NewRenderer(bundle)
	if err != nil {
		return err
	}

	h := &handlers{
		db:           db,
		cfg:          cfg,
		auth:         authSvc,
		customers:    storefront.NewCustomerRepo(db),
		branchRepo:   storefront.NewBranchRepo(db),
		addressRepo:  storefront.NewAddressRepo(db),
		favoriteRepo: storefront.NewFavoriteRepo(db),
		render:       renderer,
		bundle:       bundle,

		categories: catalog.NewCategoryRepo(db),
		products:   catalog.NewProductRepo(db),
		variants:   catalog.NewVariantRepo(db),
		stock:      catalog.NewStockRepo(db),
		images:     catalog.NewImageRepo(db),

		cartRepo:  orders.NewCartRepo(db),
		ordersSvc: orders.NewService(db),
	}

	withSession := WithSession([]byte(cfg.JWTSecret))

	// The 9 screens (COZY_WEB_DESIGN.md §3).
	mux.Handle("GET /{$}", withSession(apperr.Wrap(h.shop)))
	mux.Handle("GET /catalog/{slug}", withSession(apperr.Wrap(h.shop)))
	mux.Handle("GET /product/{slug}", withSession(apperr.Wrap(h.product)))
	mux.Handle("POST /product/{slug}/cart", withSession(apperr.Wrap(h.productAddToCart)))
	mux.Handle("POST /product/{slug}/buy", withSession(apperr.Wrap(h.productBuyNow)))
	mux.Handle("GET /cart", withSession(apperr.Wrap(h.cart)))
	mux.Handle("POST /cart/items", withSession(apperr.Wrap(h.cartAddItem)))
	mux.Handle("POST /cart/items/{variantID}/increment", withSession(apperr.Wrap(h.cartIncrement)))
	mux.Handle("POST /cart/items/{variantID}/decrement", withSession(apperr.Wrap(h.cartDecrement)))
	mux.Handle("GET /checkout", withSession(apperr.Wrap(h.checkoutForm)))
	mux.Handle("POST /checkout", withSession(apperr.Wrap(h.checkoutSubmit)))
	mux.Handle("GET /favorites", withSession(apperr.Wrap(h.favorites)))
	mux.Handle("GET /profile", withSession(apperr.Wrap(h.profile)))
	mux.Handle("GET /orders", withSession(apperr.Wrap(h.orders)))
	mux.Handle("POST /orders/{orderID}/repeat", withSession(apperr.Wrap(h.repeatOrder)))
	mux.Handle("GET /branches", withSession(apperr.Wrap(h.branches)))
	mux.Handle("GET /lang", withSession(apperr.Wrap(h.langScreen)))
	mux.Handle("POST /lang", withSession(apperr.Wrap(h.setLang)))
	mux.Handle("GET /order/{orderNumber}/done", withSession(apperr.Wrap(h.done)))

	// Web login: cookie wrapper over the existing OTP service, extended
	// with the 3rd step (name-for-a-new-customer) this task adds.
	mux.Handle("POST /login/otp/request", withSession(apperr.Wrap(h.loginRequestOTP)))
	mux.Handle("POST /login/otp/verify", withSession(apperr.Wrap(h.loginVerifyOTP)))
	mux.Handle("POST /login/name", withSession(apperr.Wrap(h.loginSetName)))
	mux.Handle("POST /logout", withSession(apperr.Wrap(h.logout)))

	// Favorites: HTMX-only mutation endpoints (remove / add-to-cart) that
	// swap the grid/toast in place — see internal/web/favorites_handlers.go.
	mux.Handle("POST /favorites/{id}", withSession(apperr.Wrap(h.favAdd)))
	mux.Handle("DELETE /favorites/{id}", withSession(apperr.Wrap(h.favRemove)))
	mux.Handle("POST /favorites/{id}/cart", withSession(apperr.Wrap(h.favAddToCart)))

	// Delivery addresses: full page + HTMX CRUD partials — see
	// internal/web/addresses_handlers.go. Route order matters here: the
	// static "/addresses/new" and "/addresses/cancel" must be registered
	// so they aren't shadowed by "/addresses/{id}/edit" — net/http's
	// ServeMux already prefers the more specific (literal-segment)
	// pattern over one with a wildcard in the same position, so this is
	// just for readability, not correctness.
	mux.Handle("GET /addresses", withSession(apperr.Wrap(h.addressesScreen)))
	mux.Handle("GET /addresses/new", withSession(apperr.Wrap(h.addressNewForm)))
	mux.Handle("GET /addresses/cancel", withSession(apperr.Wrap(h.addressCancelForm)))
	mux.Handle("GET /addresses/{id}/edit", withSession(apperr.Wrap(h.addressEditForm)))
	mux.Handle("POST /addresses", withSession(apperr.Wrap(h.addressCreate)))
	mux.Handle("POST /addresses/{id}", withSession(apperr.Wrap(h.addressUpdate)))
	mux.Handle("DELETE /addresses/{id}", withSession(apperr.Wrap(h.addressDelete)))

	// SEO + static assets.
	mux.Handle("GET /sitemap.xml", apperr.Wrap(h.sitemap))
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/static/robots.txt")
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))

	return nil
}
