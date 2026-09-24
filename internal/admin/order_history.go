package admin

import (
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// OrderHistoryData is the part of the order detail page owned by the
// orders-integrity work: the status-change log, the delivery fee line and
// the "refund required" warning. Embedded in OrderDetailData.
type OrderHistoryData struct {
	History          []OrderHistoryRowView
	DeliveryFeeLabel string // "" when the order has no delivery fee
	RefundRequired   bool
}

// OrderHistoryRowView is one order_status_history row as shown in admin.
type OrderHistoryRowView struct {
	DateLabel string
	FromLabel string // "" for the initial 'placed' row
	ToLabel   string
	Actor     string
	Note      string
}

func buildOrderHistoryData(t tr, o *orders.Order) OrderHistoryData {
	d := OrderHistoryData{RefundRequired: o.RefundRequired}
	if o.DeliveryFee > 0 {
		d.DeliveryFeeLabel = formatSom(o.DeliveryFee)
	}
	for _, h := range o.History {
		row := OrderHistoryRowView{
			DateLabel: h.CreatedAt.Format("02.01.2006 15:04"),
			ToLabel:   orderStatusMetaFor(t, h.ToStatus).Label,
			Actor:     historyActorLabel(t, h),
		}
		if h.FromStatus != nil {
			row.FromLabel = orderStatusMetaFor(t, *h.FromStatus).Label
		}
		if h.Note != nil {
			row.Note = *h.Note
		}
		d.History = append(d.History, row)
	}
	return d
}

func historyActorLabel(t tr, h orders.StatusChange) string {
	switch h.ActorType {
	case orders.ActorCustomer:
		return t.T("admin.history.actor_customer")
	case orders.ActorSystem:
		return t.T("admin.history.actor_system")
	case orders.ActorStaff:
		if h.ActorName != nil && *h.ActorName != "" {
			return *h.ActorName
		}
		return t.T("admin.history.actor_staff")
	default:
		return string(h.ActorType)
	}
}
