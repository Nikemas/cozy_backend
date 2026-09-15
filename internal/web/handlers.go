package web

import (
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

const langCookieName = "cozy_lang"

type handlers struct {
	db           *sql.DB
	cfg          *config.Config
	auth         *auth.Service
	customers    *storefront.CustomerRepo
	carts        *orders.CartRepo
	orderService *orders.Service
	bundle       *i18n.Bundle
	render       *Renderer
}

// ProfileData backs profile.gohtml's unauthenticated login form. Step is
// "" (phone entry) or "otp" (code entry). This is deliberately thin —
// just enough to exercise the real internal/auth OTP service end-to-end
// and prove the session cookie round-trips, per Task 1's verification
// criteria. Task 4 replaces it with the real HTMX-driven 3-step flow
// (phone → otp → name-for-a-new-customer).
type ProfileData struct {
	Step  string
	Phone string
}

// resolveLang reads the language cookie Foundation's /lang screen writes,
// falling back to i18n.DefaultLang.
func (h *handlers) resolveLang(r *http.Request) string {
	if c, err := r.Cookie(langCookieName); err == nil {
		if c.Value == i18n.LangRU || c.Value == i18n.LangKY {
			return c.Value
		}
	}
	return i18n.DefaultLang
}

// base builds the PageData every screen shares (header/aside state).
// Cart/favorites/orders counts are left at 0 — Tasks 2, 4 and 5 own those
// domains and wire real counts once their repos have working bodies.
func (h *handlers) base(r *http.Request, screen string) PageData {
	data := PageData{
		Lang:   h.resolveLang(r),
		Screen: screen,
	}

	customerID := CustomerID(r)
	if customerID == "" {
		return data
	}
	data.Authed = true
	data.CustomerID = customerID

	customer, err := h.customers.GetByID(r.Context(), customerID)
	if err != nil {
		// A DB hiccup here shouldn't 500 the whole page — the visitor is
		// still "authed" as far as the cookie goes, just without a name/
		// phone to show.
		slog.Warn("web: failed to load customer for session", "customer_id", customerID, "err", err)
		return data
	}
	if customer != nil {
		if customer.Name != nil {
			data.CustomerName = *customer.Name
		}
		data.MaskedPhone = maskPhone(customer.Phone)
	}

	// CartCount backs the header badge — Task 3 owns internal/orders, so
	// it's the one wiring this up now that CartRepo.List has a real body;
	// FavCount/OrdersCount stay at 0 until Tasks 4/5 do the same for their
	// own repos.
	if items, err := h.carts.List(r.Context(), customerID); err != nil {
		slog.Warn("web: failed to load cart count for header badge", "customer_id", customerID, "err", err)
	} else {
		for _, it := range items {
			data.CartCount += it.Qty
		}
	}

	return data
}

func (h *handlers) shop(w http.ResponseWriter, r *http.Request) error {
	return h.render.Render(w, "shop", h.base(r, "shop"))
}

func (h *handlers) product(w http.ResponseWriter, r *http.Request) error {
	return h.render.Render(w, "product", h.base(r, "product"))
}

// cart, checkout and done handlers live in cart_handlers.go /
// checkout_handlers.go (Task 3 — Cart + Checkout + Done).

func (h *handlers) favorites(w http.ResponseWriter, r *http.Request) error {
	return h.render.Render(w, "fav", h.base(r, "fav"))
}

func (h *handlers) profile(w http.ResponseWriter, r *http.Request) error {
	data := h.base(r, "profile")
	data.Data = ProfileData{
		Step:  r.URL.Query().Get("step"),
		Phone: r.URL.Query().Get("phone"),
	}
	return h.render.Render(w, "profile", data)
}

func (h *handlers) orders(w http.ResponseWriter, r *http.Request) error {
	return h.render.Render(w, "orders", h.base(r, "orders"))
}

func (h *handlers) branches(w http.ResponseWriter, r *http.Request) error {
	return h.render.Render(w, "branches", h.base(r, "branches"))
}

func (h *handlers) langScreen(w http.ResponseWriter, r *http.Request) error {
	return h.render.Render(w, "lang", h.base(r, "lang"))
}

// setLang stores the chosen language in a long-lived cookie. Foundation
// only needs the route + a working {{t}} round-trip in both languages;
// Task 4 may replace/extend this with persistence on customers for a
// logged-in visitor.
func (h *handlers) setLang(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return apperr.BadRequest("bad_request", "некорректная форма")
	}
	lang := r.FormValue("lang")
	if lang != i18n.LangRU && lang != i18n.LangKY {
		lang = i18n.DefaultLang
	}
	http.SetCookie(w, &http.Cookie{
		Name:     langCookieName,
		Value:    lang,
		Path:     "/",
		MaxAge:   int((365 * 24 * time.Hour).Seconds()),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/lang", http.StatusSeeOther)
	return nil
}

// loginRequestOTP re-uses internal/auth's OTP service directly — no
// SMS-sending or code-generation logic lives here, per web-plan
// Architecture Decisions ("Foundation adds a cookie wrapper over the
// existing OTP handler ... not duplicating the business logic").
func (h *handlers) loginRequestOTP(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return apperr.BadRequest("bad_request", "некорректная форма")
	}
	phone := r.FormValue("phone")

	if err := h.auth.RequestOTP(r.Context(), phone); err != nil {
		return err
	}

	data := h.base(r, "profile")
	data.Data = ProfileData{Step: "otp", Phone: phone}
	return h.render.Render(w, "profile", data)
}

// loginVerifyOTP calls the same auth.Service.VerifyOTP the mobile JSON API
// uses, then sets the access token as an httpOnly cookie (instead of
// returning it as JSON — Task 4 owns the richer web auth UX).
func (h *handlers) loginVerifyOTP(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return apperr.BadRequest("bad_request", "некорректная форма")
	}
	phone := r.FormValue("phone")
	code := r.FormValue("code")

	access, _, err := h.auth.VerifyOTP(r.Context(), phone, code)
	if err != nil {
		return err
	}

	setSessionCookie(w, access, sessionCookieTTL)
	http.Redirect(w, r, "/profile", http.StatusSeeOther)
	return nil
}

func (h *handlers) logout(w http.ResponseWriter, r *http.Request) error {
	clearSessionCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
	return nil
}
