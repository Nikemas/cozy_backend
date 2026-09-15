package admin

import (
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// staffResolver is the subset of *staff.Service the auth-gate depends on —
// defined as an interface, mirroring the staffGetter/sessionStore pattern
// already used in internal/staff, so requireStaffRole can be tested with a
// fake session lookup instead of a live database. *staff.Service satisfies
// this via the StaffFromRequest method added alongside this task.
type staffResolver interface {
	StaffFromRequest(r *http.Request) (*staff.Staff, error)
}

// requireStaffRole returns HTML-appropriate admin auth-gate middleware —
// the page-rendering sibling of (*staff.Service).RequireRole, which
// answers JSON API requests with a 401/403 body that makes no sense for a
// page a person is looking at in a browser:
//
//   - No session cookie, or an unknown/expired/revoked/deactivated one ->
//     redirect to /admin/login.
//   - A valid session whose role isn't in roles -> redirect to the first
//     admin page that role IS allowed to see (firstAllowedPath), so a
//     manager hitting /admin/staff directly lands on /admin/orders instead
//     of a dead end, and a role with no visible page at all (point_staff,
//     this wave) lands on noAccessPath rather than looping.
//   - Otherwise the wrapped handler runs, with the staff member attached
//     to the request context via staff.NewContextWithStaff — retrievable
//     with staff.FromContext exactly as the JSON API's RequireRole already
//     makes available, so Tasks 2-5 read the current staff member the same
//     way regardless of which gate protected the route.
//
// Reused as-is by Tasks 2-5 (same package) for their own routes — this
// isn't Foundation-only plumbing they need to reimplement.
func requireStaffRole(resolver staffResolver, roles ...staff.Role) func(http.HandlerFunc) http.HandlerFunc {
	allowed := make(map[staff.Role]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}

	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			st, err := resolver.StaffFromRequest(r)
			if err != nil {
				http.Error(w, "внутренняя ошибка", http.StatusInternalServerError)
				return
			}
			if st == nil {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}
			if !allowed[st.Role] {
				http.Redirect(w, r, firstAllowedPath(st.Role), http.StatusSeeOther)
				return
			}

			next(w, r.WithContext(staff.NewContextWithStaff(r.Context(), st)))
		}
	}
}
