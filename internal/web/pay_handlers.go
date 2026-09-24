// Online payment pages: where the payment provider (Bakai, or the mock
// checkout) sends the customer back — GET /pay/return/{orderID} — plus its
// HTMX status poll and the "pay again" action.
package web

import (
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/payments"
)

// Polling: the status fragment reloads every payPollInterval seconds, at
// most payPollMax times (~2 minutes), then asks the customer to refresh.
const (
	payPollInterval = 2
	payPollMax      = 60
)

// Payment result states shown by pay_return.gohtml.
const (
	payStatePaid      = "paid"
	payStatePending   = "pending"
	payStateFailed    = "failed"
	payStateCancelled = "cancelled" // order cancelled (payment cancelled or expired)
	payStateUnknown   = "unknown"   // no session, or not this customer's order
)

// PayReturnData backs pay_return.gohtml.
type PayReturnData struct {
	OrderID     string
	OrderNumber string
	Total       float64
	State       string
	// Anonymous: the visitor has no site session for this order — most
	// often a mobile-app customer whose payment page ran in an in-app
	// browser. They get only the "back to the app" button, no details.
	Anonymous bool
	// Retryable: "Оплатить снова" is offered (orders.PaymentRetryable).
	Retryable bool
	// Poll: the status fragment keeps polling every PollEverySec seconds;
	// PollURL is its next URL.
	Poll         bool
	PollURL      string
	PollEverySec int
	// TimedOut: polling gave up while the payment was still pending.
	TimedOut bool
	// AppLink is the app's deep link to this order (cozy://orders/<id>);
	// ShowAppLink: offered to anonymous visitors and phone browsers.
	// template.URL because html/template would otherwise blank the
	// non-http cozy:// scheme; it is only ever built from a validated UUID.
	AppLink     template.URL
	ShowAppLink bool
}

// AppOrderLink is the mobile app's deep link to an order ("" unless
// orderID is a UUID).
func AppOrderLink(orderID string) template.URL {
	if uuid.Validate(orderID) != nil {
		return ""
	}
	return template.URL("cozy://orders/" + orderID) //nolint:gosec // fixed scheme+path, id validated as a UUID above
}

// payState maps an order to the result page's state.
func payState(o *orders.Order) string {
	if o.Status == orders.StatusCancelled {
		return payStateCancelled
	}
	if o.PaymentStatus == nil {
		return payStateUnknown
	}
	switch *o.PaymentStatus {
	case orders.PaymentPaid, orders.PaymentRefunded:
		return payStatePaid
	case orders.PaymentPending:
		return payStatePending
	case orders.PaymentFailed:
		return payStateFailed
	default:
		return payStateCancelled
	}
}

// buildPayReturnData is the pure part of the result page: o is the
// customer's own order or nil (anonymous / not theirs); poll is how many
// status polls have run.
func buildPayReturnData(orderID string, o *orders.Order, poll int, mobile bool) PayReturnData {
	d := PayReturnData{OrderID: orderID, AppLink: AppOrderLink(orderID), ShowAppLink: mobile}
	if o == nil {
		d.Anonymous = true
		d.State = payStateUnknown
		d.ShowAppLink = d.AppLink != ""
		return d
	}
	d.OrderID = o.ID
	d.AppLink = AppOrderLink(o.ID)
	d.OrderNumber = o.OrderNumber
	d.Total = o.TotalAmount
	d.State = payState(o)
	retryable := orders.PaymentRetryable(*o)
	switch d.State {
	case payStatePending:
		if poll < payPollMax {
			d.Poll = true
			d.PollEverySec = payPollInterval
			d.PollURL = payments.ReturnPath(o.ID) + "/status?n=" + strconv.Itoa(poll+1)
		} else {
			d.TimedOut = true
			d.Retryable = retryable
		}
	case payStateFailed:
		d.Retryable = retryable
	}
	return d
}

// isMobileUA: a phone/tablet browser, where the app deep link may work.
func isMobileUA(r *http.Request) bool {
	ua := r.UserAgent()
	for _, s := range []string{"Android", "iPhone", "iPad", "iPod"} {
		if strings.Contains(ua, s) {
			return true
		}
	}
	return false
}

// loadPayOrder returns the logged-in customer's own online order, or nil
// when there is no session or the order isn't theirs (the page then shows
// the anonymous "back to the app" state rather than an error: the bank
// sends app customers here without a site session).
func (h *handlers) loadPayOrder(r *http.Request, orderID string) (*orders.Order, error) {
	customerID := CustomerID(r)
	if customerID == "" || uuid.Validate(orderID) != nil {
		return nil, nil
	}
	o, err := h.ordersSvc.GetOrder(r.Context(), customerID, orderID)
	if err != nil {
		var appErr *apperr.AppError
		if errors.As(err, &appErr) && appErr.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return o, nil
}

// payReturn serves GET /pay/return/{orderID}: the payment result page.
func (h *handlers) payReturn(w http.ResponseWriter, r *http.Request) error {
	orderID := r.PathValue("orderID")
	o, err := h.loadPayOrder(r, orderID)
	if err != nil {
		return err
	}
	if o != nil && o.PaymentMethod != orders.PaymentOnlineCard {
		http.Redirect(w, r, "/order/"+o.OrderNumber+"/done", http.StatusSeeOther)
		return nil
	}
	w.Header().Set("Cache-Control", "no-store")
	data := h.base(r, "pay_return")
	data.Data = buildPayReturnData(orderID, o, 0, isMobileUA(r))
	return h.render.Render(w, "pay_return", data)
}

// payReturnStatus serves GET /pay/return/{orderID}/status?n=K: just the
// status fragment, swapped in by HTMX every payPollInterval seconds.
func (h *handlers) payReturnStatus(w http.ResponseWriter, r *http.Request) error {
	orderID := r.PathValue("orderID")
	o, err := h.loadPayOrder(r, orderID)
	if err != nil {
		return err
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n < 0 {
		n = 0
	}
	w.Header().Set("Cache-Control", "no-store")
	data := h.base(r, "pay_return")
	data.Data = buildPayReturnData(orderID, o, n, isMobileUA(r))
	return h.render.RenderPartial(w, "pay_return", "pay_status", data)
}

// payRetry serves POST /pay/{orderID}/retry ("Оплатить снова"): opens a
// new payment attempt and sends the customer to the provider's page. An
// order that can no longer be paid goes back to its result page.
func (h *handlers) payRetry(w http.ResponseWriter, r *http.Request) error {
	customerID := CustomerID(r)
	if customerID == "" {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return nil
	}
	if h.paySvc == nil {
		return payments.ErrNotConfigured
	}
	orderID := r.PathValue("orderID")
	if uuid.Validate(orderID) != nil {
		return apperr.NotFound("order_not_found", "заказ не найден")
	}
	url, err := h.paySvc.RetryPayment(r.Context(), customerID, orderID)
	if err != nil {
		var appErr *apperr.AppError
		if errors.As(err, &appErr) && appErr.Code == orders.ErrPaymentNotRetryable.Code {
			http.Redirect(w, r, payments.ReturnPath(orderID), http.StatusSeeOther)
			return nil
		}
		return err
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
	return nil
}
