package web

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Nikemas/cozy_backend/internal/auth"
)

const (
	sessionCookieName = "cozy_session"

	// sessionCookieTTL mirrors internal/auth's private accessTokenTTL (15
	// minutes) — the cookie just carries that same access token, so its
	// lifetime has to match. If that constant ever changes, update this
	// too (or export it from internal/auth alongside ParseAccessToken).
	sessionCookieTTL = 15 * time.Minute
)

type ctxKey int

const customerIDKey ctxKey = iota

// WithSession reads the httpOnly JWT cookie set at login, validates it via
// auth.ParseAccessToken — the same parser other packages' future
// JWT-auth middleware will use — and stores the customer ID in the
// request context when valid. It never rejects a request outright: most
// storefront pages are public, and handlers that require a logged-in
// customer check CustomerID(r) themselves and redirect to /profile.
func WithSession(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie(sessionCookieName); err == nil {
				customerID, err := auth.ParseAccessToken(secret, c.Value)
				if err != nil {
					slog.Debug("web: invalid or expired session cookie", "err", err)
				} else {
					r = r.WithContext(context.WithValue(r.Context(), customerIDKey, customerID))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CustomerID returns the authenticated customer's ID from the request
// context, or "" if the request carries no valid session cookie.
func CustomerID(r *http.Request) string {
	v, _ := r.Context().Value(customerIDKey).(string)
	return v
}

// setSessionCookie writes accessToken as an httpOnly cookie — the exact
// JWT internal/auth issues for the mobile OTP flow, so a customer
// authenticated on the app is recognized on the site too, per web-plan
// Architecture Decisions.
func setSessionCookie(w http.ResponseWriter, accessToken string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    accessToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   false, // TODO(prod): true once the site is served HTTPS-only
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
