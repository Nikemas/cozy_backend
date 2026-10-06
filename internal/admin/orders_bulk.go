// orders_bulk.go (fix/admin-ops, W5): bulk status change from the orders
// list. Every order goes through orders.Service.AdminUpdateStatus on its
// own — the same path as the detail page's buttons, so the state machine,
// stock return on cancel, status history (which is also the audit
// journal's record of it) and customer notifications all apply — with
// the same RBAC as orderStatusUpdate. Failures are reported per order.
package admin

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// maxBulkFailuresShown caps the per-order failure lines on the list.
const maxBulkFailuresShown = 20

// BulkStatusOption is one <option> of the orders bulk bar's status select.
type BulkStatusOption struct {
	Value string
	Label string
}

// bulkStatusOptions are the target statuses the bulk bar offers a role
// (cancelling is owner-only, as on the detail page).
func bulkStatusOptions(t tr, role staff.Role) []BulkStatusOption {
	all := []orders.OrderStatus{orders.StatusConfirmed, orders.StatusCourierAssigned, orders.StatusDelivered, orders.StatusCancelled}
	out := make([]BulkStatusOption, 0, len(all))
	for _, s := range all {
		if s == orders.StatusCancelled && role != staff.RoleOwner {
			continue
		}
		out = append(out, BulkStatusOption{Value: string(s), Label: orderStatusMetaFor(t, s).Label})
	}
	return out
}

// orderBulkStatus handles POST /admin/orders/bulk-status: status + the
// checked rows' ids (form field "id"). Redirects back to the list with a
// toast (how many changed) and one bulk_fail entry per failed order —
// both as keys (toast.go, bulkFailure), never as text.
func (h *handlers) orderBulkStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)
	t := h.tr(r)
	if err := r.ParseForm(); err != nil {
		redirectWithToast(w, r, "/admin/orders", toastKey("form_error"))
		return
	}
	back := safeReturnURL(r.FormValue("back"), "/admin/orders")

	ids, errToast := bulkIDs(r.Form["id"])
	if errToast.Key != "" {
		redirectWithToast(w, r, back, errToast)
		return
	}

	newStatus := orders.OrderStatus(r.FormValue("status"))
	allowed := false
	for _, o := range bulkStatusOptions(t, st.Role) {
		if o.Value == string(newStatus) {
			allowed = true
		}
	}
	if !allowed {
		f := toastKey("bulk_choose_status")
		if newStatus == orders.StatusCancelled {
			f = toastKey("cancel_forbidden")
		}
		redirectWithToast(w, r, back, f)
		return
	}

	changed := 0
	var failures []bulkFailure
	for _, id := range ids {
		if fail := h.bulkUpdateOne(r, st, id, newStatus); fail != nil {
			failures = append(failures, *fail)
			continue
		}
		changed++
	}

	target := back
	if len(failures) > 0 {
		more := 0
		if len(failures) > maxBulkFailuresShown {
			more = len(failures) - maxBulkFailuresShown
			failures = failures[:maxBulkFailuresShown]
		}
		sep := "?"
		if strings.Contains(back, "?") {
			sep = "&"
		}
		target = back + sep + encodeBulkFailures(failures, more).Encode()
	}
	redirectWithToast(w, r, target, flash{Key: "bulk_status", Status: newStatus, N: changed, Of: len(ids), hasN: true})
}

// bulkUpdateOne changes one order's status with orderStatusUpdate's RBAC:
// point_staff only for orders of its own point (others read as "не
// найден", not leaking their existence). It returns nil on success, else
// why it failed (keyed — see bulkFailure).
func (h *handlers) bulkUpdateOne(r *http.Request, st *staff.Staff, id string, newStatus orders.OrderStatus) *bulkFailure {
	ctx := r.Context()
	order, err := h.ordersSvc.AdminGetOrder(ctx, id)
	if err != nil || !staffCanSeeOrder(st, order) {
		return &bulkFailure{Number: id[:8], Reason: bulkReasonNotFound}
	}
	if order.Status == newStatus {
		return &bulkFailure{Number: order.OrderNumber, Reason: bulkReasonAlready}
	}
	if _, err := h.ordersSvc.AdminUpdateStatus(ctx, id, newStatus); err != nil {
		var appErr *apperr.AppError
		if errors.As(err, &appErr) {
			return &bulkFailure{Number: order.OrderNumber, Code: appErr.Code}
		}
		slog.ErrorContext(ctx, "admin: bulk order status failed", "order_id", id, "err", err)
		return &bulkFailure{Number: order.OrderNumber, Reason: bulkReasonFailed}
	}
	return nil
}

// ---------- bulk failures in the redirect URL ----------

// Query params for the per-order failure lines on the orders list.
const (
	bulkFailParam = "bulk_fail"
	bulkMoreParam = "bulk_more"
)

// Fixed failure reasons; anything else is an apperr code ("e.<code>").
const (
	bulkReasonNotFound = "not_found"
	bulkReasonAlready  = "already"
	bulkReasonFailed   = "failed"
	bulkCodePrefix     = "e."
	bulkFailSep        = "~"
)

// bulkReasonKeys maps a fixed reason to its locale key.
var bulkReasonKeys = map[string]string{
	bulkReasonNotFound: "admin.apperr.order_not_found",
	bulkReasonAlready:  "admin.bulk.already_in_status",
	bulkReasonFailed:   "admin.order.status_change_failed",
}

// orderNumberPattern is what may appear as the order in a failure line:
// an order number (COZY-20260914-001) or the id prefix of an unknown one.
var orderNumberPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,40}$`)

// bulkFailure is one order a bulk status change skipped. It travels in the
// URL as "<number>~<reason>" (or "<number>~e.<apperr code>") so the list
// renders it from locale keys — a crafted link can't add text.
type bulkFailure struct {
	Number string
	Reason string // one of bulkReasonKeys, or "" when Code is set
	Code   string // apperr code
}

func (f bulkFailure) encode() string {
	reason := f.Reason
	if f.Code != "" {
		reason = bulkCodePrefix + f.Code
	}
	return f.Number + bulkFailSep + reason
}

// encodeBulkFailures is the bulk_fail (+ bulk_more) query for failures.
func encodeBulkFailures(failures []bulkFailure, more int) url.Values {
	v := url.Values{}
	for _, f := range failures {
		v.Add(bulkFailParam, f.encode())
	}
	if more > 0 {
		v.Set(bulkMoreParam, strconv.Itoa(more))
	}
	return v
}

// bulkFailureNotes renders q's bulk_fail entries as list notes; entries
// that don't parse are dropped.
func bulkFailureNotes(t tr, q url.Values) []string {
	var notes []string
	for i, raw := range q[bulkFailParam] {
		if i == maxBulkFailuresShown {
			break
		}
		if note, ok := bulkFailureNote(t, raw); ok {
			notes = append(notes, note)
		}
	}
	if n, ok := parseToastCount(q.Get(bulkMoreParam)); ok && n > 0 {
		notes = append(notes, t.F("admin.bulk.and_more", n))
	}
	return notes
}

func bulkFailureNote(t tr, raw string) (string, bool) {
	number, reason, ok := strings.Cut(raw, bulkFailSep)
	if !ok || !orderNumberPattern.MatchString(number) {
		return "", false
	}
	var msg string
	if code, isCode := strings.CutPrefix(reason, bulkCodePrefix); isCode {
		if !apperrCodePattern.MatchString(code) {
			return "", false
		}
		msg = apperrCodeMessage(t, code)
	} else {
		key, known := bulkReasonKeys[reason]
		if !known {
			return "", false
		}
		msg = t.T(key)
	}
	return fmt.Sprintf("№ %s: %s", number, msg), true
}
