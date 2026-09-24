package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// OrderItemView backs one line of an order card on orders.gohtml.
type OrderItemView struct {
	Title    string // e.g. "Размер 42, чёрный"
	Qty      int
	PhotoURL string // thumbnail; "" → shoe icon
}

// OrderView backs one order card on orders.gohtml. StatusKey is an i18n
// key (translated in-template with {{t .StatusKey}}, same as every other
// static screen label) rather than an already-translated string, so the
// order list stays correct if the visitor's language cookie changes.
type OrderView struct {
	ID          string
	Number      string
	DateLabel   string
	StatusKey   string
	StatusClass string // CSS modifier, e.g. "order-status--transit"
	TotalLabel  string
	Items       []OrderItemView
	// ShowTrack/ShowRepeat pick which action button the card shows:
	// in-flight orders (placed/confirmed/courier_assigned) get "Отследить"
	// (there's no real courier tracking yet, so it just surfaces the
	// current status — see statusView), finished orders (delivered/
	// cancelled) get "Повторить" to re-add their items to the cart.
	ShowTrack  bool
	ShowRepeat bool
	// ShowCancel: the customer may still cancel (placed, not paid online).
	ShowCancel bool
}

// OrdersData backs orders.gohtml.
type OrdersData struct {
	Orders []OrderView
	Empty  bool
}

// orderStatusMeta describes how one OrderStatus renders: its i18n key, CSS
// modifier, and whether it's still "in flight" (see OrderView.ShowTrack).
type orderStatusMeta struct {
	Key      string
	Class    string
	InFlight bool
}

// statusView maps orders.OrderStatus (internal/orders/order.go) to display
// metadata. Kept here rather than in internal/orders because it's
// presentation (i18n keys + CSS classes), not domain logic — internal/
// orders stays untouched per Task 5's read-only contract with it.
func statusView(s orders.OrderStatus) orderStatusMeta {
	switch s {
	case orders.StatusPlaced:
		return orderStatusMeta{Key: "order.status.placed", Class: "order-status--placed", InFlight: true}
	case orders.StatusConfirmed:
		return orderStatusMeta{Key: "order.status.confirmed", Class: "order-status--confirmed", InFlight: true}
	case orders.StatusCourierAssigned:
		return orderStatusMeta{Key: "order.status.courier_assigned", Class: "order-status--transit", InFlight: true}
	case orders.StatusDelivered:
		return orderStatusMeta{Key: "order.status.delivered", Class: "order-status--delivered", InFlight: false}
	case orders.StatusCancelled:
		return orderStatusMeta{Key: "order.status.cancelled", Class: "order-status--cancelled", InFlight: false}
	default:
		return orderStatusMeta{Key: "order.status.placed", Class: "order-status--placed", InFlight: false}
	}
}

// buildOrderViews turns Service.ListOrders' result into orders.gohtml's
// view model. t translates a single i18n key (bound to the request's
// language) — used for the small bits of item-line copy ("размер ...")
// that aren't whole static template strings.
// photos maps variant_id → thumbnail URL (see variantPhotos); nil is fine.
func buildOrderViews(list []orders.Order, t func(string) string, photos map[string]string) []OrderView {
	views := make([]OrderView, 0, len(list))
	for _, o := range list {
		meta := statusView(o.Status)

		items := make([]OrderItemView, 0, len(o.Items))
		for _, it := range o.Items {
			title := it.ProductNameSnapshot
			if it.SizeSnapshot != "" || it.ColorSnapshot != "" {
				title = fmt.Sprintf("%s — %s %s, %s", it.ProductNameSnapshot, t("order.item.size_prefix"), it.SizeSnapshot, strings.ToLower(it.ColorSnapshot))
			}
			items = append(items, OrderItemView{Title: title, Qty: it.Quantity, PhotoURL: photos[it.VariantID]})
		}

		views = append(views, OrderView{
			ID:          o.ID,
			Number:      o.OrderNumber,
			DateLabel:   o.CreatedAt.Format("02.01.2006"),
			StatusKey:   meta.Key,
			StatusClass: meta.Class,
			TotalLabel:  formatAmount(o.TotalAmount, t("common.currency")),
			Items:       items,
			ShowTrack:   meta.InFlight,
			ShowRepeat:  !meta.InFlight,
			ShowCancel:  orders.CustomerCanCancel(o),
		})
	}
	return views
}

// isNotImplemented reports whether err is the 501 apperr placeholder that
// internal/orders currently returns from every method (Task 3 hasn't
// landed a real implementation yet in this worktree). Handlers use it to
// degrade gracefully — empty state / "Скоро" toast — instead of a raw 500.
func isNotImplemented(err error) bool {
	var appErr *apperr.AppError
	return errors.As(err, &appErr) && appErr.Status == http.StatusNotImplemented
}

func (h *handlers) orders(w http.ResponseWriter, r *http.Request) error {
	data := h.base(r, "orders")
	t := func(key string) string { return h.render.T(data.Lang, key) }

	view := OrdersData{Empty: true}
	if customerID := CustomerID(r); customerID != "" {
		list, err := h.ordersSvc.ListOrders(r.Context(), customerID)
		switch {
		case err == nil:
			var variantIDs []string
			for _, o := range list {
				for _, it := range o.Items {
					variantIDs = append(variantIDs, it.VariantID)
				}
			}
			photos, perr := h.variantPhotos(r.Context(), variantIDs)
			if perr != nil {
				return perr
			}
			view.Orders = buildOrderViews(list, t, photos)
			view.Empty = len(view.Orders) == 0
		case isNotImplemented(err):
			// internal/orders (Task 3) isn't merged yet in this worktree —
			// render the empty state rather than a 500, per web-plan's
			// coordination note for Task 5.
			view.Empty = true
		default:
			return err
		}
	}

	switch r.URL.Query().Get("repeat") {
	case "added":
		data.Toast = t("toast.repeat_added")
	case "soon":
		data.Toast = t("toast.repeat_soon")
	}
	switch r.URL.Query().Get("cancel") {
	case "done":
		data.Toast = t("toast.order_cancelled")
	case "failed":
		data.Toast = t("toast.order_not_cancellable")
	}

	data.Data = view
	return h.render.Render(w, "orders", data)
}

// repeatOrder backs orders.gohtml's "Повторить" button: it re-adds every
// line of a past order to the customer's cart via orders.CartRepo.Add,
// called strictly by the contract Task 3 owns — this never reimplements
// CartRepo or Service.CreateOrder. If internal/orders still 501s (Task 3
// not merged into this worktree yet), it degrades to a "Скоро" toast
// instead of a hard error, per the coordination note in tasks/web-plan.md.
func (h *handlers) repeatOrder(w http.ResponseWriter, r *http.Request) error {
	customerID := CustomerID(r)
	if customerID == "" {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return nil
	}

	orderID := r.PathValue("orderID")
	result := "added"

	order, err := h.ordersSvc.GetOrder(r.Context(), customerID, orderID)
	if err != nil {
		if !isNotImplemented(err) {
			return err
		}
		result = "soon"
	} else {
		for _, item := range order.Items {
			if err := h.cartRepo.Add(r.Context(), customerID, item.VariantID, item.Quantity); err != nil {
				if !isNotImplemented(err) {
					return err
				}
				result = "soon"
				break
			}
		}
	}

	http.Redirect(w, r, "/orders?repeat="+result, http.StatusSeeOther)
	return nil
}
