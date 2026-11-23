package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/httpmw"
	"github.com/Nikemas/cozy_backend/internal/support"
)

// Support pages (feat/support-tickets): /support is the request form
// (guests give a name and phone; a logged-in customer's are prefilled)
// plus, for a logged-in customer, their requests; /support/{id} is one
// request's conversation. Plain HTML forms — they work without JS.

// supportService is what the support pages use of *support.Service.
type supportService interface {
	Create(ctx context.Context, in support.CreateInput) (*support.Ticket, error)
	ListForCustomer(ctx context.Context, customerID string, page, pageSize int) ([]support.Summary, int, error)
	GetForCustomer(ctx context.Context, customerID, id string) (*support.Ticket, error)
	AddCustomerMessage(ctx context.Context, customerID, id, body string) (*support.Ticket, error)
}

const (
	// supportListSize is how many of their requests a customer sees on
	// /support (newest activity first).
	supportListSize = support.MaxPageSize
	// supportOrderOptions caps the "which order" select.
	supportOrderOptions = 20
	// supportGuestPerHour bounds guest requests per client IP (on top of
	// the per-phone limit in internal/support).
	supportGuestPerHour = 5
	// supportSentParam carries a guest's new request number to the
	// "request sent" note after the redirect.
	supportSentParam = "sent"
)

// newSupportGuestLimiter allows supportGuestPerHour guest requests per IP
// per hour (a token bucket refilled over the hour).
func newSupportGuestLimiter() *httpmw.KeyedLimiter {
	return httpmw.NewKeyedLimiter(httpmw.RateLimitConfig{
		RPS:   float64(supportGuestPerHour) / time.Hour.Seconds(),
		Burst: supportGuestPerHour,
	})
}

// SupportOption is one <option>.
type SupportOption struct {
	Value    string
	Label    string
	Selected bool
}

// SupportTicketRow is one of the customer's requests in the list.
type SupportTicketRow struct {
	ID          string
	NumberLabel string
	TopicLabel  string
	StatusKey   string
	StatusClass string
	DateLabel   string
	Preview     string
}

// SupportData backs support.gohtml.
type SupportData struct {
	Topics  []SupportOption
	Orders  []SupportOption
	Name    string
	Phone   string
	Message string
	Error   string
	// SentLabel is the "request №N accepted" note shown to a guest after
	// sending ("" = none).
	SentLabel  string
	Tickets    []SupportTicketRow
	MaxMessage int
	MaxName    int
}

// SupportMessageView is one message of the conversation.
type SupportMessageView struct {
	Mine      bool // written by the customer
	Text      string
	DateLabel string
}

// SupportTicketData backs support_ticket.gohtml.
type SupportTicketData struct {
	ID          string
	NumberLabel string
	TopicLabel  string
	StatusKey   string
	StatusClass string
	DateLabel   string
	OrderNumber string
	Messages    []SupportMessageView
	Closed      bool
	Draft       string
	Error       string
	MaxMessage  int
}

const supportDateLayout = "02.01.2006 15:04"

// supportStatusView maps a request status to its label key and the
// order-status chip modifier it reuses.
func supportStatusView(s support.Status) (key, class string) {
	switch s {
	case support.StatusInProgress:
		return "support.status.in_progress", "order-status--transit"
	case support.StatusClosed:
		return "support.status.closed", "order-status--delivered"
	default:
		return "support.status.new", "order-status--placed"
	}
}

func (h *handlers) supportTopicOptions(lang string, selected support.Topic) []SupportOption {
	opts := make([]SupportOption, 0, len(support.Topics))
	for _, t := range support.Topics {
		opts = append(opts, SupportOption{Value: string(t), Label: h.t(lang, "support.topic."+string(t)), Selected: t == selected})
	}
	return opts
}

func (h *handlers) supportNumberLabel(lang string, n int64) string {
	return fmt.Sprintf(h.t(lang, "support.number"), n)
}

// supportForm renders GET /support.
func (h *handlers) supportForm(w http.ResponseWriter, r *http.Request) error {
	page := h.base(r, "support")
	d := SupportData{Name: page.CustomerName, Phone: page.CustomerPhone}
	if n, err := strconv.ParseInt(r.URL.Query().Get(supportSentParam), 10, 64); err == nil && n > 0 && !page.Authed {
		d.SentLabel = fmt.Sprintf(h.t(page.Lang, "support.sent"), n)
	}
	return h.renderSupport(w, r, http.StatusOK, page, d, "", "")
}

// renderSupport fills the parts of SupportData that come from the
// database (topics, the customer's orders and requests) and renders.
func (h *handlers) renderSupport(w http.ResponseWriter, r *http.Request, status int, page PageData, d SupportData, topic support.Topic, orderID string) error {
	d.Topics = h.supportTopicOptions(page.Lang, topic)
	d.MaxMessage, d.MaxName = support.MaxMessageLen, support.MaxNameLen
	if page.Authed {
		d.Orders = h.supportOrderOptions(r.Context(), page.CustomerID, orderID)
		list, _, err := h.support.ListForCustomer(r.Context(), page.CustomerID, 1, supportListSize)
		if err != nil {
			return err
		}
		d.Tickets = h.supportRows(page.Lang, list)
	}
	page.Data = d
	return h.render.RenderStatus(w, status, "support", page)
}

func (h *handlers) supportRows(lang string, list []support.Summary) []SupportTicketRow {
	rows := make([]SupportTicketRow, 0, len(list))
	for _, s := range list {
		key, class := supportStatusView(s.Status)
		rows = append(rows, SupportTicketRow{
			ID: s.ID, NumberLabel: h.supportNumberLabel(lang, s.Number),
			TopicLabel: h.t(lang, "support.topic."+string(s.Topic)),
			StatusKey:  key, StatusClass: class,
			DateLabel: s.UpdatedAt.Format(supportDateLayout),
			Preview:   truncateText(s.LastMessage.Body, 120),
		})
	}
	return rows
}

// supportOrderOptions lists the customer's recent orders for the "which
// order" select; a failure only hides the select.
func (h *handlers) supportOrderOptions(ctx context.Context, customerID, selected string) []SupportOption {
	if h.ordersSvc == nil {
		return nil
	}
	list, err := h.ordersSvc.ListOrders(ctx, customerID)
	if err != nil {
		slog.WarnContext(ctx, "web: support form orders unavailable", "err", err)
		return nil
	}
	if len(list) > supportOrderOptions {
		list = list[:supportOrderOptions]
	}
	opts := make([]SupportOption, 0, len(list))
	for _, o := range list {
		opts = append(opts, SupportOption{
			Value: o.ID, Label: o.OrderNumber + " · " + o.CreatedAt.Format("02.01.2006"), Selected: o.ID == selected,
		})
	}
	return opts
}

// supportSubmit handles POST /support.
func (h *handlers) supportSubmit(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return apperr.BadRequest("bad_request", "некорректная форма").WithVariant("form")
	}
	page := h.base(r, "support")
	in := support.CreateInput{
		CustomerID: page.CustomerID,
		Name:       r.PostFormValue("name"),
		Phone:      r.PostFormValue("phone"),
		Topic:      support.Topic(r.PostFormValue("topic")),
		Message:    r.PostFormValue("message"),
		OrderID:    r.PostFormValue("order_id"),
		Channel:    support.ChannelWeb,
	}
	retry := SupportData{Name: in.Name, Phone: in.Phone, Message: in.Message}

	if !page.Authed && !h.allowGuestTicket(r) {
		retry.Error = h.t(page.Lang, "error.code.too_many_tickets")
		return h.renderSupport(w, r, http.StatusTooManyRequests, page, retry, in.Topic, in.OrderID)
	}

	t, err := h.support.Create(r.Context(), in)
	if err != nil {
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Status >= http.StatusInternalServerError {
			return err
		}
		retry.Error = h.errorText(page.Lang, ae)
		return h.renderSupport(w, r, ae.Status, page, retry, in.Topic, in.OrderID)
	}
	if page.Authed {
		http.Redirect(w, r, "/support/"+t.ID, http.StatusSeeOther)
		return nil
	}
	http.Redirect(w, r, "/support?"+url.Values{supportSentParam: {strconv.FormatInt(t.Number, 10)}}.Encode(), http.StatusSeeOther)
	return nil
}

// allowGuestTicket spends one of the client IP's guest-request tokens.
func (h *handlers) allowGuestTicket(r *http.Request) bool {
	key := httpmw.ClientIPFromContext(r.Context())
	if key == "" {
		key = r.RemoteAddr
	}
	ok, _ := h.supportGuests.Allow(key)
	return ok
}

// supportTicket renders GET /support/{id}: one of the customer's own
// requests (anyone else's, or a guest visit, is the 404 page).
func (h *handlers) supportTicket(w http.ResponseWriter, r *http.Request) error {
	page := h.base(r, "support_ticket")
	if !page.Authed {
		return redirectToLogin(w, r)
	}
	t, err := h.support.GetForCustomer(r.Context(), page.CustomerID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return h.renderSupportTicket(w, http.StatusOK, page, t, "", "")
}

func (h *handlers) renderSupportTicket(w http.ResponseWriter, status int, page PageData, t *support.Ticket, draft, errMsg string) error {
	key, class := supportStatusView(t.Status)
	msgs := make([]SupportMessageView, 0, len(t.Messages))
	for _, m := range t.Messages {
		msgs = append(msgs, SupportMessageView{
			Mine: m.Author == support.AuthorCustomer, Text: m.Body, DateLabel: m.CreatedAt.Format(supportDateLayout),
		})
	}
	page.Data = SupportTicketData{
		ID: t.ID, NumberLabel: h.supportNumberLabel(page.Lang, t.Number),
		TopicLabel: h.t(page.Lang, "support.topic."+string(t.Topic)),
		StatusKey:  key, StatusClass: class, DateLabel: t.CreatedAt.Format(supportDateLayout),
		OrderNumber: t.OrderNumber, Messages: msgs, Closed: t.Status == support.StatusClosed,
		Draft: draft, Error: errMsg, MaxMessage: support.MaxMessageLen,
	}
	return h.render.RenderStatus(w, status, "support_ticket", page)
}

// supportReply handles POST /support/{id}/messages.
func (h *handlers) supportReply(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return apperr.BadRequest("bad_request", "некорректная форма").WithVariant("form")
	}
	page := h.base(r, "support_ticket")
	if !page.Authed {
		return redirectToLogin(w, r)
	}
	id := r.PathValue("id")
	body := r.PostFormValue("message")
	if _, err := h.support.AddCustomerMessage(r.Context(), page.CustomerID, id, body); err != nil {
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Status >= http.StatusInternalServerError || ae.Status == http.StatusNotFound {
			return err
		}
		t, gerr := h.support.GetForCustomer(r.Context(), page.CustomerID, id)
		if gerr != nil {
			return gerr
		}
		return h.renderSupportTicket(w, ae.Status, page, t, body, h.errorText(page.Lang, ae))
	}
	http.Redirect(w, r, "/support/"+id+"#reply", http.StatusSeeOther)
	return nil
}
