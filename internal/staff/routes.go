package staff

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// sessionCookieMaxAge mirrors sessionTTL, in seconds, for the Set-Cookie
// Max-Age attribute.
var sessionCookieMaxAge = int(sessionTTL.Seconds())

// SessionCookieName and SessionCookieMaxAge re-export the private
// sessionCookieName/sessionCookieMaxAge above so internal/admin's HTML
// login/logout (which can't set a JSON body, so can't reuse the
// POST /admin/api/login handler below directly) can set/read the exact
// same cookie — same name, same Max-Age — and stay compatible with
// sessions this package's own JSON login/logout create and consume.
const SessionCookieName = sessionCookieName

var SessionCookieMaxAge = sessionCookieMaxAge

// SetSessionCookie re-exports setSessionCookie below for internal/admin's
// HTML login/logout, so both surfaces set the cookie with identical
// attributes (HttpOnly/Secure/SameSite/Path) instead of a second
// hand-copied http.SetCookie call risking drift from this one.
func SetSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	setSessionCookie(w, token, maxAge)
}

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

	// Admin staff CRUD — owner only. RequireRole checks the session cookie
	// and role before any of these handlers run.
	ownerOnly := svc.RequireRole(RoleOwner)
	mux.Handle("GET /admin/api/staff", ownerOnly(apperr.Wrap(listStaffHandler(svc))))
	mux.Handle("POST /admin/api/staff", ownerOnly(apperr.Wrap(createStaffHandler(svc))))
	mux.Handle("PUT /admin/api/staff/{id}", ownerOnly(apperr.Wrap(updateStaffHandler(svc))))
}

// staffResponse is the JSON shape of one staff account in admin API
// responses. It's a handler-local DTO built with explicit fields rather
// than json tags on the Staff domain struct itself, so password_hash is
// excluded by construction — there's no field to forget to tag "-" on.
type staffResponse struct {
	ID        string    `json:"id"`
	Phone     string    `json:"phone"`
	Name      string    `json:"name"`
	Role      Role      `json:"role"`
	PointID   *string   `json:"point_id,omitempty"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
}

func newStaffResponse(s Staff) staffResponse {
	return staffResponse{
		ID:        s.ID,
		Phone:     s.Phone,
		Name:      s.Name,
		Role:      s.Role,
		PointID:   s.PointID,
		IsActive:  s.IsActive,
		CreatedAt: s.CreatedAt,
	}
}

type staffListResponse struct {
	Items []staffResponse `json:"items"`
}

func listStaffHandler(svc *Service) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		list, err := svc.ListStaff(r.Context())
		if err != nil {
			return err
		}
		items := make([]staffResponse, len(list))
		for i, st := range list {
			items[i] = newStaffResponse(st)
		}
		return writeJSON(w, http.StatusOK, staffListResponse{Items: items})
	}
}

// createStaffRequest is what POST /admin/api/staff decodes into. Password
// is plaintext on the wire (over HTTPS) — Service.CreateStaff hashes it
// with bcrypt before it's ever passed to the repo.
type createStaffRequest struct {
	Phone    string  `json:"phone"`
	Password string  `json:"password"`
	Name     string  `json:"name"`
	Role     Role    `json:"role"`
	PointID  *string `json:"point_id"`
}

func createStaffHandler(svc *Service) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req createStaffRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return apperr.BadRequest("bad_request", "некорректное тело запроса")
		}

		st, err := svc.CreateStaff(r.Context(), CreateStaffInput(req))
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusCreated, newStaffResponse(*st))
	}
}

// updateStaffRequest is what PUT /admin/api/staff/{id} decodes into.
// Password is a pointer: omitted (nil) means "leave the password
// unchanged", an empty string is rejected by Service.UpdateStaff.
type updateStaffRequest struct {
	Name     string  `json:"name"`
	Role     Role    `json:"role"`
	PointID  *string `json:"point_id"`
	IsActive bool    `json:"is_active"`
	Password *string `json:"password"`
}

func updateStaffHandler(svc *Service) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req updateStaffRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return apperr.BadRequest("bad_request", "некорректное тело запроса")
		}

		st, err := svc.UpdateStaff(r.Context(), r.PathValue("id"), UpdateStaffInput(req))
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, newStaffResponse(*st))
	}
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
