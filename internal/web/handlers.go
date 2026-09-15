package web

import (
	"database/sql"
	"errors"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/catalog"
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
	branchRepo   *storefront.BranchRepo
	addressRepo  *storefront.AddressRepo
	favoriteRepo *storefront.FavoriteRepo
	render       *Renderer
	bundle       *i18n.Bundle

	// Catalog domain (Task B, already merged to main) — Task 2 only calls
	// these, it doesn't implement them. See internal/catalog/*.go.
	categories *catalog.CategoryRepo
	products   *catalog.ProductRepo
	variants   *catalog.VariantRepo
	stock      *catalog.StockRepo
	images     *catalog.ImageRepo

	// Orders domain — Task 3's real implementation now. Named cartRepo
	// (not cart) to avoid colliding with the /cart screen handler below.
	cartRepo  *orders.CartRepo
	ordersSvc *orders.Service
}

// t translates key into lang — used by handlers that need a translated
// string outside of template execution (e.g. an HTMX toast fragment
// written directly to the response instead of through a .gohtml file).
func (h *handlers) t(lang, key string) string {
	return h.bundle.T(lang, key)
}

// ProfileData backs profile.gohtml's unauthenticated 3-step login form
// (rendered via the shared _profile_auth partial): Step is "" (phone
// entry), "otp" (code entry) or "name" (display name for a brand-new
// phone number — internal/storefront.CustomerRepo has no name on file
// yet). Error carries an inline, human-readable message for the current
// step (invalid code, rate limit, ...) instead of bouncing the visitor
// out to a raw JSON error response.
type ProfileData struct {
	Step  string
	Phone string
	Error string
}

// isHX reports whether r was issued by htmx (hx-post/hx-get/...) rather
// than a plain browser navigation — handlers that support both a no-JS
// fallback and a partial-swap flow branch on this to decide whether to
// render a single fragment or the whole layout.
func isHX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// errMessage extracts a human-readable message from err — an
// *apperr.AppError's Message if there is one, otherwise a generic
// fallback that never leaks internal error text to a customer-facing
// page.
func errMessage(err error) string {
	var appErr *apperr.AppError
	if errors.As(err, &appErr) {
		return appErr.Message
	}
	return "Что-то пошло не так, попробуйте ещё раз"
}

// toastOOB renders msg as an out-of-band HTMX swap targeting
// "toast-slot" (see _toast.gohtml) — any HTMX handler can append this
// after its main fragment to pop a toast without the caller having to
// hx-target the toast container explicitly.
func toastOOB(msg string) string {
	return `<div id="toast-slot" hx-swap-oob="innerHTML"><div class="toast"><i class="ti ti-circle-check"></i> ` +
		html.EscapeString(msg) + `</div></div>`
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

// base builds the PageData every screen shares (header/aside state,
// stats tiles). FavCount is real (this task owns internal/storefront).
// OrdersCount calls internal/orders.Service.ListOrders — owned by Task 3
// — and is left at 0 if that call errors (e.g. still a 501 stub in this
// worktree), so a not-yet-implemented orders domain never breaks the
// profile page.
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
	} else if customer != nil {
		if customer.Name != nil {
			data.CustomerName = *customer.Name
		}
		data.MaskedPhone = maskPhone(customer.Phone)
	}

	if n, ferr := h.favoriteRepo.Count(r.Context(), customerID); ferr != nil {
		slog.Debug("web: favorites count unavailable", "err", ferr)
	} else {
		data.FavCount = n
	}

	if list, oerr := h.ordersSvc.ListOrders(r.Context(), customerID); oerr != nil {
		slog.Debug("web: orders count unavailable", "err", oerr)
	} else {
		data.OrdersCount = len(list)
	}

	if items, cerr := h.cartRepo.List(r.Context(), customerID); cerr != nil {
		slog.Warn("web: failed to load cart count for header badge", "customer_id", customerID, "err", cerr)
	} else {
		for _, it := range items {
			data.CartCount += it.Qty
		}
	}

	return data
}

// shop and product are implemented in catalog_view.go (Task 2) — kept out
// of this file so handlers.go stays the thin per-screen dispatch table
// Foundation set up.

// cart, checkout and done handlers live in cart_handlers.go /
// checkout_handlers.go (Task 3 — Cart + Checkout + Done).

func (h *handlers) profile(w http.ResponseWriter, r *http.Request) error {
	data := h.base(r, "profile")
	if !data.Authed {
		data.Data = ProfileData{
			Step:  r.URL.Query().Get("step"),
			Phone: r.URL.Query().Get("phone"),
		}
	}
	return h.render.Render(w, "profile", data)
}

// h.orders and h.branches now live in orders_handlers.go / branches_handlers.go.

func (h *handlers) langScreen(w http.ResponseWriter, r *http.Request) error {
	return h.render.Render(w, "lang", h.base(r, "lang"))
}

// setLang stores the chosen language in a long-lived cookie. Task 1
// (Foundation) left this open for Task 4 to also persist the choice on
// customers for a logged-in visitor; the site works fully off the cookie
// alone (the mobile app doesn't share web language state per the tech
// spec), so this stays cookie-only — a `lang` column on customers would
// be a schema change with no consumer yet.
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

// renderProfileAuth renders the current login-flow step: just the
// _profile_auth fragment for an HTMX request (so it can hx-swap the
// auth card in place), or the whole profile page for a plain form
// submission (no-JS fallback).
func (h *handlers) renderProfileAuth(w http.ResponseWriter, r *http.Request, data ProfileData) error {
	page := PageData{Lang: h.resolveLang(r), Screen: "profile", Data: data}
	if isHX(r) {
		return h.render.RenderPartial(w, "profile", "_profile_auth", page)
	}
	return h.render.Render(w, "profile", page)
}

// loginRequestOTP re-uses internal/auth's OTP service directly — no
// SMS-sending or code-generation logic lives here, per web-plan
// Architecture Decisions ("Foundation adds a cookie wrapper over the
// existing OTP handler ... not duplicating the business logic"). Errors
// (bad phone, rate limit) are shown inline on the phone step rather than
// surfaced as a raw JSON error page.
func (h *handlers) loginRequestOTP(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return apperr.BadRequest("bad_request", "некорректная форма")
	}
	phone := r.FormValue("phone")

	if err := h.auth.RequestOTP(r.Context(), phone); err != nil {
		return h.renderProfileAuth(w, r, ProfileData{Step: "", Phone: phone, Error: errMessage(err)})
	}

	return h.renderProfileAuth(w, r, ProfileData{Step: "otp", Phone: phone})
}

// loginVerifyOTP calls the same auth.Service.VerifyOTP the mobile JSON
// API uses, then sets the access token as an httpOnly cookie (instead of
// returning it as JSON). If the phone belongs to a brand-new customer
// (no name on file), it doesn't send the visitor straight to /profile —
// it shows the third step ("как вас зовут") first; loginSetName below
// completes onboarding. An invalid/expired code is shown inline on the
// same otp step rather than as a raw JSON error.
func (h *handlers) loginVerifyOTP(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return apperr.BadRequest("bad_request", "некорректная форма")
	}
	phone := r.FormValue("phone")
	code := r.FormValue("code")

	access, _, _, err := h.auth.VerifyOTP(r.Context(), phone, code)
	if err != nil {
		return h.renderProfileAuth(w, r, ProfileData{Step: "otp", Phone: phone, Error: errMessage(err)})
	}

	setSessionCookie(w, access, sessionCookieTTL)

	customerID, err := auth.ParseAccessToken([]byte(h.cfg.JWTSecret), access)
	if err != nil {
		// Extremely unlikely (we just issued this token), but fail safe by
		// sending the visitor to a fresh /profile load rather than 500ing.
		slog.Warn("web: failed to parse freshly issued access token", "err", err)
		return h.redirectOrHXRedirect(w, r, "/profile")
	}

	customer, cerr := h.customers.GetByID(r.Context(), customerID)
	needsName := cerr == nil && (customer == nil || customer.Name == nil || strings.TrimSpace(*customer.Name) == "")
	if cerr != nil {
		slog.Warn("web: failed to load customer right after OTP verify", "customer_id", customerID, "err", cerr)
	}

	if needsName {
		return h.renderProfileAuth(w, r, ProfileData{Step: "name", Phone: phone})
	}

	return h.redirectOrHXRedirect(w, r, "/profile")
}

// loginSetName completes the 3rd login step for a brand-new customer:
// the session cookie is already set (from loginVerifyOTP, on the
// previous response), so CustomerID(r) resolves from the request's own
// cookie here.
func (h *handlers) loginSetName(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return apperr.BadRequest("bad_request", "некорректная форма")
	}
	customerID := CustomerID(r)
	if customerID == "" {
		return apperr.Unauthorized("unauthorized", "сессия истекла, войдите заново")
	}

	name := r.FormValue("name")
	if err := h.customers.SetName(r.Context(), customerID, name); err != nil {
		return h.renderProfileAuth(w, r, ProfileData{Step: "name", Error: errMessage(err)})
	}

	return h.redirectOrHXRedirect(w, r, "/profile")
}

// redirectOrHXRedirect sends a plain 303 redirect for a normal form
// submission, or an HX-Redirect header for an htmx request — htmx
// performs a full client-side navigation on that header instead of
// swapping the (would-be full-layout) response body into a fragment
// target, which would otherwise nest one page's <html> inside another's.
func (h *handlers) redirectOrHXRedirect(w http.ResponseWriter, r *http.Request, url string) error {
	if isHX(r) {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusOK)
		return nil
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
	return nil
}

func (h *handlers) logout(w http.ResponseWriter, r *http.Request) error {
	clearSessionCookie(w)
	return h.redirectOrHXRedirect(w, r, "/")
}
