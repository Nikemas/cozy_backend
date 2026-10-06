// owner_ux.go (fix/admin-owner-ux) holds small helpers behind the shop
// owner's daily-flow fixes: products list paging, the unprocessed-orders
// badge, and order-detail contact links.
package admin

import (
	"context"
	"database/sql"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// productsListPath is the Товары list screen.
const productsListPath = "/admin/products"

// productsStaleParams are one-shot query params (a toast, a bulk
// result) that must not follow the user onto another page of the list.
var productsStaleParams = []string{"toast", "bulk_fail"}

// productsPageURL is the products list URL for page, keeping every filter
// in q (category, subcategory, search, point, out-of-stock, page size).
// q is not modified.
func productsPageURL(q url.Values, page int) string {
	next := url.Values{}
	for k, v := range q {
		next[k] = append([]string(nil), v...)
	}
	for _, k := range productsStaleParams {
		next.Del(k)
	}
	if page > 1 {
		next.Set("page", strconv.Itoa(page))
	} else {
		next.Del("page")
	}
	if len(next) == 0 {
		return productsListPath
	}
	return productsListPath + "?" + next.Encode()
}

// ---------- unprocessed-orders badge ----------

// newOrdersCounter counts orders still waiting for the shop (status
// "Оформлен"), optionally at one point of sale only.
type newOrdersCounter interface {
	CountNewOrders(ctx context.Context, pointID *string) (int, error)
}

// orderBadgeRepo is the SQL newOrdersCounter (idx_orders_status keeps it
// an index-only count).
type orderBadgeRepo struct {
	db *sql.DB
}

func (r orderBadgeRepo) CountNewOrders(ctx context.Context, pointID *string) (int, error) {
	const base = `SELECT COUNT(*) FROM orders WHERE status = 'placed'`
	var n int
	var err error
	if pointID == nil {
		err = r.db.QueryRowContext(ctx, base).Scan(&n)
	} else {
		err = r.db.QueryRowContext(ctx, base+` AND point_id = $1`, *pointID).Scan(&n)
	}
	return n, err
}

// badgeWriter carries the new-orders count from withNavBadges to
// Renderer.Render (same channel as langWriter for the language).
type badgeWriter struct {
	http.ResponseWriter
	newOrders int
}

func (bw *badgeWriter) adminNewOrders() int { return bw.newOrders }

// adminLang keeps the language langWriter attached underneath visible.
func (bw *badgeWriter) adminLang() string { return langFromWriter(bw.ResponseWriter) }

// Unwrap lets http.ResponseController reach the underlying writer.
func (bw *badgeWriter) Unwrap() http.ResponseWriter { return bw.ResponseWriter }

// newOrdersFromWriter returns the count withNavBadges attached to w, or 0.
func newOrdersFromWriter(w http.ResponseWriter) int {
	if bw, ok := w.(interface{ adminNewOrders() int }); ok {
		return bw.adminNewOrders()
	}
	return 0
}

// withNavBadges counts the staff member's unprocessed orders for page
// views (GET/HEAD) so the sidebar's «Заказы» item and the mobile menu
// button can show it on every screen — new orders used to be visible only
// by opening the orders list. point_staff counts its own point only (no
// point: nothing). Must run inside requireStaffRole (needs the staff in
// the context). A failed count only drops the badge.
func withNavBadges(counter newOrdersCounter) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				next(w, r)
				return
			}
			n, err := countNewOrdersFor(r.Context(), counter)
			if err != nil {
				slog.WarnContext(r.Context(), "admin nav: new orders count failed", "err", err)
			}
			next(&badgeWriter{ResponseWriter: w, newOrders: n}, r)
		}
	}
}

func countNewOrdersFor(ctx context.Context, counter newOrdersCounter) (int, error) {
	st, ok := staff.FromContext(ctx)
	if !ok || st == nil || counter == nil {
		return 0, nil
	}
	var pointID *string
	if st.Role == staff.RolePointStaff {
		if st.PointID == nil {
			return 0, nil
		}
		pointID = st.PointID
	}
	n, err := counter.CountNewOrders(ctx, pointID)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// withOrdersBadge returns a copy of items with the «Заказы» entry
// carrying n as its badge.
func withOrdersBadge(items []NavItem, n int) []NavItem {
	out := make([]NavItem, len(items))
	copy(out, items)
	for i := range out {
		if out[i].Key == "orders" {
			out[i].Badge = n
		}
	}
	return out
}

// chainMiddleware applies outer around inner: outer(inner(next)).
func chainMiddleware(outer, inner func(http.HandlerFunc) http.HandlerFunc) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc { return outer(inner(next)) }
}

// withPlacedCount returns a copy of chips with the «Оформлен» chip
// carrying n (the same count as the sidebar badge).
func withPlacedCount(chips []StatusChipLink, n int) []StatusChipLink {
	out := make([]StatusChipLink, len(chips))
	copy(out, chips)
	for i := range out {
		if out[i].Class == "admin-chip--placed" {
			out[i].Count = n
		}
	}
	return out
}

// ---------- order detail: contact links ----------

// kgPhoneDigits is the digit count of a full Kyrgyz number, 996XXXXXXXXX.
const kgPhoneDigits = 12

// PhoneContact is a customer phone ready for the order screen: grouped
// for reading, plus tap-to-call and WhatsApp links (staff mostly work
// from a phone and call or message the customer to confirm an order).
type PhoneContact struct {
	Raw     string // as stored, for the copy button
	Display string
	// Tel is "tel:+996…" — a template.URL because html/template only
	// passes http(s)/mailto links; it is built from digits only.
	Tel      template.URL
	WhatsApp string // "https://wa.me/996…"
}

// phoneContact builds the links from a stored phone. A Kyrgyz number is
// grouped as "+996 700 111 222"; anything else is shown as stored.
func phoneContact(raw string) PhoneContact {
	var digits strings.Builder
	for _, c := range raw {
		if c >= '0' && c <= '9' {
			digits.WriteRune(c)
		}
	}
	d := digits.String()
	if d == "" {
		return PhoneContact{}
	}
	tel := template.URL("tel:+" + d) //nolint:gosec // built from digits only
	pc := PhoneContact{Raw: raw, Display: raw, Tel: tel, WhatsApp: "https://wa.me/" + d}
	if len(d) == kgPhoneDigits && strings.HasPrefix(d, "996") {
		pc.Display = "+996 " + d[3:6] + " " + d[6:9] + " " + d[9:]
	}
	return pc
}

// displayPhone is phoneContact's grouped form, or raw when it has no digits.
func displayPhone(raw string) string {
	if d := phoneContact(raw).Display; d != "" {
		return d
	}
	return raw
}
