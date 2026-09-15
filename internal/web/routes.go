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
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/i18n"
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
		db:        db,
		cfg:       cfg,
		auth:      authSvc,
		customers: storefront.NewCustomerRepo(db),
		render:    renderer,
	}

	withSession := WithSession([]byte(cfg.JWTSecret))

	// The 9 screens (COZY_WEB_DESIGN.md §3).
	mux.Handle("GET /{$}", withSession(apperr.Wrap(h.shop)))
	mux.Handle("GET /catalog/{slug}", withSession(apperr.Wrap(h.shop)))
	mux.Handle("GET /product/{slug}", withSession(apperr.Wrap(h.product)))
	mux.Handle("GET /cart", withSession(apperr.Wrap(h.cart)))
	mux.Handle("GET /favorites", withSession(apperr.Wrap(h.favorites)))
	mux.Handle("GET /profile", withSession(apperr.Wrap(h.profile)))
	mux.Handle("GET /orders", withSession(apperr.Wrap(h.orders)))
	mux.Handle("GET /branches", withSession(apperr.Wrap(h.branches)))
	mux.Handle("GET /lang", withSession(apperr.Wrap(h.langScreen)))
	mux.Handle("POST /lang", withSession(apperr.Wrap(h.setLang)))
	mux.Handle("GET /order/{orderNumber}/done", withSession(apperr.Wrap(h.done)))

	// Web login: cookie wrapper over the existing OTP service.
	mux.Handle("POST /login/otp/request", withSession(apperr.Wrap(h.loginRequestOTP)))
	mux.Handle("POST /login/otp/verify", withSession(apperr.Wrap(h.loginVerifyOTP)))
	mux.Handle("POST /logout", withSession(apperr.Wrap(h.logout)))

	// SEO + static assets.
	mux.Handle("GET /sitemap.xml", apperr.Wrap(h.sitemap))
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/static/robots.txt")
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))

	return nil
}
