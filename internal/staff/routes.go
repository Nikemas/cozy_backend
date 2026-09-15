package staff

import (
	"encoding/json"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// sessionCookieMaxAge mirrors sessionTTL, in seconds, for the Set-Cookie
// Max-Age attribute.
var sessionCookieMaxAge = int(sessionTTL.Seconds())

// RegisterRoutes mounts the staff login/logout flow under /admin/api/*.
func RegisterRoutes(mux *http.ServeMux, svc *Service) {
	mux.Handle("POST /admin/api/login", apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var req struct {
			Phone    string `json:"phone"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return apperr.BadRequest("bad_request", "некорректное тело запроса")
		}

		token, err := svc.Login(r.Context(), req.Phone, req.Password)
		if err != nil {
			return err
		}

		setSessionCookie(w, token, sessionCookieMaxAge)
		return writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))

	mux.Handle("POST /admin/api/logout", apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
			if err := svc.Logout(r.Context(), cookie.Value); err != nil {
				return err
			}
		}

		// Clear the cookie regardless of whether one was present, so a
		// client with a stale/invalid cookie still ends up logged out.
		setSessionCookie(w, "", -1)
		return writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
}

func setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(body)
}
