package staff

import (
	"context"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// sessionCookieName is the httpOnly cookie set on login and cleared on
// logout, carrying the raw (unhashed) session token.
const sessionCookieName = "staff_session"

type ctxKey int

const staffCtxKey ctxKey = iota

// FromContext returns the staff member RequireRole attached to the request
// context, if the request passed through it.
func FromContext(ctx context.Context) (*Staff, bool) {
	st, ok := ctx.Value(staffCtxKey).(*Staff)
	return st, ok
}

// RequireRole returns middleware that resolves the staff_session cookie to
// an active session and staff member, then allows the request through only
// if that staff member's role is one of roles.
//
//   - No cookie, an unknown/expired/revoked session, or a deactivated staff
//     account -> apperr.Unauthorized (401).
//   - A valid session whose role isn't in roles -> apperr.Forbidden (403).
//   - Otherwise the wrapped handler runs, with the staff member available
//     via FromContext.
func (s *Service) RequireRole(roles ...Role) func(http.Handler) http.Handler {
	allowed := make(map[Role]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}

	return func(next http.Handler) http.Handler {
		return apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil || cookie.Value == "" {
				return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
			}

			st, err := s.authenticatedStaff(r.Context(), cookie.Value)
			if err != nil {
				return err
			}
			if st == nil {
				return apperr.Unauthorized("unauthenticated", "сессия недействительна или истекла")
			}

			if !allowed[st.Role] {
				return apperr.Forbidden("forbidden", "недостаточно прав для этого действия")
			}

			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), staffCtxKey, st)))
			return nil
		})
	}
}
