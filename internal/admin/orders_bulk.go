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
func bulkStatusOptions(role staff.Role) []BulkStatusOption {
	all := []orders.OrderStatus{orders.StatusConfirmed, orders.StatusCourierAssigned, orders.StatusDelivered, orders.StatusCancelled}
	out := make([]BulkStatusOption, 0, len(all))
	for _, s := range all {
		if s == orders.StatusCancelled && role != staff.RoleOwner {
			continue
		}
		out = append(out, BulkStatusOption{Value: string(s), Label: orderStatusMetaFor(s).Label})
	}
	return out
}

// orderBulkStatus handles POST /admin/orders/bulk-status: status + the
// checked rows' ids (form field "id"). Redirects back to the list with a
// toast (how many changed) and one bulk_fail line per failed order.
func (h *handlers) orderBulkStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _ := staff.FromContext(ctx)
	if err := r.ParseForm(); err != nil {
		redirectWithToast(w, r, "/admin/orders", "не удалось прочитать форму")
		return
	}
	back := safeReturnURL(r.FormValue("back"), "/admin/orders")

	ids, errMsg := bulkIDs(r.Form["id"])
	if errMsg != "" {
		redirectWithToast(w, r, back, errMsg)
		return
	}

	newStatus := orders.OrderStatus(r.FormValue("status"))
	allowed := false
	for _, o := range bulkStatusOptions(st.Role) {
		if o.Value == string(newStatus) {
			allowed = true
		}
	}
	if !allowed {
		msg := "Выберите статус"
		if newStatus == orders.StatusCancelled {
			msg = "только владелец может отменить заказ"
		}
		redirectWithToast(w, r, back, msg)
		return
	}

	changed := 0
	var failures []string
	for _, id := range ids {
		label, err := h.bulkUpdateOne(r, st, id, newStatus)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", label, err.Error()))
			continue
		}
		changed++
	}

	toast := fmt.Sprintf("Статус «%s» установлен: %d из %d", orderStatusMetaFor(newStatus).Label, changed, len(ids))
	target := back
	if len(failures) > 0 {
		v := url.Values{}
		for i, f := range failures {
			if i == maxBulkFailuresShown {
				v.Add("bulk_fail", fmt.Sprintf("… и ещё %d", len(failures)-maxBulkFailuresShown))
				break
			}
			v.Add("bulk_fail", f)
		}
		sep := "?"
		if strings.Contains(back, "?") {
			sep = "&"
		}
		target = back + sep + v.Encode()
	}
	redirectWithToast(w, r, target, toast)
}

// bulkUpdateOne changes one order's status with orderStatusUpdate's RBAC:
// point_staff only for orders of its own point (others read as "не
// найден", not leaking their existence). label is the order number when
// known. err carries a user-facing message.
func (h *handlers) bulkUpdateOne(r *http.Request, st *staff.Staff, id string, newStatus orders.OrderStatus) (label string, err error) {
	ctx := r.Context()
	label = "Заказ " + id[:8]
	order, err := h.ordersSvc.AdminGetOrder(ctx, id)
	if err != nil || !staffCanSeeOrder(st, order) {
		return label, errors.New("заказ не найден")
	}
	label = "№ " + order.OrderNumber
	if order.Status == newStatus {
		return label, errors.New("уже в этом статусе")
	}
	if _, err := h.ordersSvc.AdminUpdateStatus(ctx, id, newStatus); err != nil {
		var appErr *apperr.AppError
		if errors.As(err, &appErr) {
			return label, errors.New(appErr.Message)
		}
		slog.ErrorContext(ctx, "admin: bulk order status failed", "order_id", id, "err", err)
		return label, errors.New("не удалось изменить статус")
	}
	return label, nil
}
