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
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/httpmw"
	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/payments"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// staticMaxAge is how long browsers may reuse /static/* without
// revalidating. Asset URLs aren't content-hashed, so this bounds how long
// a deploy's CSS/logo change can take to show; after it, revalidation is
// a cheap 304 via the ETag httpmw.Static sets.
const staticMaxAge = 10 * time.Minute

// RegisterRoutes mounts the storefront on mux. authSvc must be the same
// *auth.Service instance registerAPIRoutes uses for /api/v1/auth/* — the
// web login flow calls its RequestOTP/VerifyOTP directly rather than
// duplicating the OTP business logic. payProvider is the active online
// payment provider (the same instance the JSON API uses); nil hides
// "card online" at checkout.
func RegisterRoutes(mux *http.ServeMux, db *sql.DB, cfg *config.Config, authSvc *auth.Service, payProvider payments.Provider) error {
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
		zones:     orders.NewDeliveryZoneRepo(db),
	}
	if payProvider != nil {
		h.paySvc = payments.NewService(db, payProvider, h.ordersSvc, cfg.PaymentsBaseURL())
	}

	session := WithSession([]byte(cfg.JWTSecret))
	withSession := func(next http.Handler) http.Handler { return session(langParam(next)) }

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
	mux.Handle("POST /orders/{orderID}/cancel", withSession(apperr.Wrap(h.cancelOrder)))
	mux.Handle("GET /branches", withSession(apperr.Wrap(h.branches)))
	mux.Handle("GET /lang", withSession(apperr.Wrap(h.langScreen)))
	mux.Handle("POST /lang", withSession(apperr.Wrap(h.setLang)))
	mux.Handle("GET /order/{orderNumber}/done", withSession(apperr.Wrap(h.done)))

	// Online payment: where the bank (or the mock) sends the customer
	// back, its HTMX status poll, and "pay again" — see pay_handlers.go.
	mux.Handle("GET /pay/return/{orderID}", withSession(apperr.Wrap(h.payReturn)))
	mux.Handle("GET /pay/return/{orderID}/status", withSession(apperr.Wrap(h.payReturnStatus)))
	mux.Handle("POST /pay/{orderID}/retry", withSession(apperr.Wrap(h.payRetry)))
	mux.Handle("POST /pay/{orderID}/qr", withSession(apperr.Wrap(h.payQR)))

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

	// Legal/info pages (footer links; the mobile app opens /privacy).
	for _, name := range staticPages {
		mux.Handle("GET /"+name, withSession(apperr.Wrap(h.staticPage(name))))
	}

	// SEO + static assets.
	mux.Handle("GET /sitemap.xml", apperr.Wrap(h.sitemap))
	mux.HandleFunc("GET /robots.txt", h.robots)
	mux.Handle("GET /static/", http.StripPrefix("/static/", httpmw.Static("web/static", staticMaxAge)))

	// Catch-all: every URL nothing else matched gets the branded HTML 404
	// (or the JSON error body under /api/ — apperr.WriteError decides by
	// path). apperr renders every site-page error through renderHTMLError.
	mux.Handle("/", withSession(apperr.Wrap(h.notFound)))
	apperr.SetHTMLRenderer(h.renderHTMLError)

	return nil
}
