package admin

import (
	"strings"
	"unicode"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// SVG <path d="..."> strings copied verbatim from the design canvas's
// navDefs array (Cozy Admin.dc.html, the data-dc-script block, lines
// ~959-965) — see nav.go's navDefs below for which icon belongs to which
// screen.
const (
	ordersIconPath     = "M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z M3.27 6.96 12 12.01l8.73-5.05 M12 22.08V12"
	productsIconPath   = "M20.59 13.41 13.42 20.58a2 2 0 0 1-2.83 0L2 12V2h10l8.59 8.59a2 2 0 0 1 0 2.82z M7 7h.01"
	categoriesIconPath = "M3 3h7v7H3z M14 3h7v7h-7z M14 14h7v7h-7z M3 14h7v7H3z"
	reportsIconPath    = "M18 20V10 M12 20V4 M6 20v-4"
	pointsIconPath     = "M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z M9 22V12h6v10"
	stockIconPath      = "M21 8l-9-5-9 5v8l9 5 9-5z M3 8l9 5 9-5 M12 13v8"
	staffIconPath      = "M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2 M9 7a4 4 0 1 0 0 8 4 4 0 0 0 0-8 M23 21v-2a4 4 0 0 0-3-3.87 M16 3.13a4 4 0 0 1 0 7.75"
)

// noAccessPath is where a staff member whose role can see none of the
// admin screens below ends up (an unknown role). point_staff used to land
// here; since fix/admin it sees Заказы and Остатки of its own point.
const noAccessPath = "/admin/no-access"

// navDef is one entry from the design canvas's navDefs array — key, label,
// icon, and the roles allowed to see it (§5 ТЗ: owner sees everything
// here, manager sees orders/products/reports, point_staff sees orders and
// stock of its own point). Order matters: it's both the sidebar's display order and,
// via firstAllowedPath, the fallback page a role lands on when it hits a
// route/nav item it can't see.
type navDef struct {
	key   string
	label string // locale key (admin.nav.*), printed via {{t .Label}}
	icon  string
	roles []staff.Role
}

var navDefs = []navDef{
	{"orders", "admin.nav.orders", ordersIconPath, []staff.Role{staff.RoleOwner, staff.RoleManager, staff.RolePointStaff}},
	{"products", "admin.nav.products", productsIconPath, []staff.Role{staff.RoleOwner, staff.RoleManager}},
	{"stock", "admin.nav.stock", stockIconPath, []staff.Role{staff.RoleOwner, staff.RoleManager, staff.RolePointStaff}},
	{"categories", "admin.nav.categories", categoriesIconPath, []staff.Role{staff.RoleOwner, staff.RoleManager}},
	{"reports", "admin.nav.reports", reportsIconPath, []staff.Role{staff.RoleOwner, staff.RoleManager}},
	{"points", "admin.nav.points", pointsIconPath, []staff.Role{staff.RoleOwner}},
	{"staff", "admin.nav.staff", staffIconPath, []staff.Role{staff.RoleOwner}},
}

func roleCanSee(d navDef, role staff.Role) bool {
	for _, r := range d.roles {
		if r == role {
			return true
		}
	}
	return false
}

// navItemsForRole returns the sidebar entries role is allowed to see, in
// navDefs order, with the item whose key == activeKey marked Active — the
// server-side equivalent of the canvas's navDefs.filter(...).map(...).
func navItemsForRole(role staff.Role, activeKey string) []NavItem {
	items := make([]NavItem, 0, len(navDefs))
	for _, d := range navDefs {
		if !roleCanSee(d, role) {
			continue
		}
		items = append(items, NavItem{
			Key:      d.key,
			Label:    d.label,
			IconPath: d.icon,
			URL:      "/admin/" + d.key,
			Active:   d.key == activeKey,
		})
	}
	return items
}

// firstAllowedPath returns the first admin page role is allowed to open —
// used both as the post-login redirect target and as where the auth-gate
// sends a staff member who hits a page their role can't see directly by
// URL. A role that can't see any admin page at all (only point_staff in
// this wave) lands on noAccessPath instead of looping back to a page it
// will immediately get redirected away from again.
func firstAllowedPath(role staff.Role) string {
	for _, d := range navDefs {
		if roleCanSee(d, role) {
			return "/admin/" + d.key
		}
	}
	return noAccessPath
}

// roleLabel is the locale key (admin.role.*) of the label shown under the
// staff member's name in the sidebar footer (design canvas's
// {{ roleLabel }} binding); templates print it via {{t .RoleLabel}}. An
// unknown role is returned as-is, which {{t}} also prints as-is.
func roleLabel(role staff.Role) string {
	switch role {
	case staff.RoleOwner, staff.RoleManager, staff.RolePointStaff:
		return "admin.role." + string(role)
	default:
		return string(role)
	}
}

// initialsFor computes up to 2 uppercase initials from a staff member's
// full name (design canvas's {{ initials }} binding), e.g. "Айгерим Б." ->
// "АБ". Empty name yields an empty string rather than panicking.
func initialsFor(name string) string {
	fields := strings.Fields(name)
	var b strings.Builder
	for i, f := range fields {
		if i >= 2 {
			break
		}
		r := []rune(f)
		if len(r) > 0 {
			b.WriteRune(unicode.ToUpper(r[0]))
		}
	}
	return b.String()
}
