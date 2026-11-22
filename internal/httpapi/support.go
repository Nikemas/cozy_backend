package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/support"
)

// supportService is the subset of *support.Service the customer support
// endpoints use (a fake in handler tests). Every call is scoped to the
// authenticated customer; a request id alone never grants access.
type supportService interface {
	Create(ctx context.Context, in support.CreateInput) (*support.Ticket, error)
	ListForCustomer(ctx context.Context, customerID string, page, pageSize int) ([]support.Summary, int, error)
	GetForCustomer(ctx context.Context, customerID, id string) (*support.Ticket, error)
	AddCustomerMessage(ctx context.Context, customerID, id, body string) (*support.Ticket, error)
}

// registerSupportRoutes mounts the customer's support requests
// (feat/support-tickets) under /api/v1/support/tickets, all behind
// RequireCustomer. Requests created here have channel "app".
func registerSupportRoutes(mux *http.ServeMux, db *sql.DB, authSvc *auth.Service) {
	svc := support.NewService(db)
	rc := authSvc.RequireCustomer
	mux.Handle("POST /api/v1/support/tickets", rc(apperr.Wrap(createTicketHandler(svc))))
	mux.Handle("GET /api/v1/support/tickets", rc(apperr.Wrap(listTicketsHandler(svc))))
	mux.Handle("GET /api/v1/support/tickets/{id}", rc(apperr.Wrap(getTicketHandler(svc))))
	mux.Handle("POST /api/v1/support/tickets/{id}/messages", rc(apperr.Wrap(addTicketMessageHandler(svc))))
}

// createTicketRequest is the POST /api/v1/support/tickets body.
type createTicketRequest struct {
	Topic   string `json:"topic"`
	Message string `json:"message"`
	OrderID string `json:"order_id"`
	Name    string `json:"name"`
	Phone   string `json:"phone"`
}

type ticketMessageRequest struct {
	Message string `json:"message"`
}

type ticketMessageOut struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// ticketBase is what list rows and the full request share.
type ticketBase struct {
	ID          string     `json:"id"`
	Number      int64      `json:"number"`
	Topic       string     `json:"topic"`
	Status      string     `json:"status"`
	Channel     string     `json:"channel"`
	OrderID     *string    `json:"order_id"`
	OrderNumber *string    `json:"order_number"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ClosedAt    *time.Time `json:"closed_at"`
}

type ticketSummaryOut struct {
	ticketBase
	MessagesCount int              `json:"messages_count"`
	LastMessage   ticketMessageOut `json:"last_message"`
}

type ticketOut struct {
	ticketBase
	Name     string             `json:"name"`
	Phone    string             `json:"phone"`
	Messages []ticketMessageOut `json:"messages"`
}

type ticketListResponse struct {
	Items    []ticketSummaryOut `json:"items"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
	Total    int                `json:"total"`
}

func orderNumberPtr(n string) *string {
	if n == "" {
		return nil
	}
	return &n
}

func newMessageOut(m support.Message) ticketMessageOut {
	return ticketMessageOut{ID: m.ID, Author: string(m.Author), Text: m.Body, CreatedAt: m.CreatedAt}
}

func newTicketOut(t *support.Ticket) ticketOut {
	msgs := make([]ticketMessageOut, len(t.Messages))
	for i, m := range t.Messages {
		msgs[i] = newMessageOut(m)
	}
	return ticketOut{
		ticketBase: ticketBase{
			ID: t.ID, Number: t.Number, Topic: string(t.Topic), Status: string(t.Status), Channel: string(t.Channel),
			OrderID: t.OrderID, OrderNumber: orderNumberPtr(t.OrderNumber),
			CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, ClosedAt: t.ClosedAt,
		},
		Name: t.Name, Phone: t.Phone, Messages: msgs,
	}
}

func newTicketSummaryOut(s support.Summary) ticketSummaryOut {
	return ticketSummaryOut{
		ticketBase: ticketBase{
			ID: s.ID, Number: s.Number, Topic: string(s.Topic), Status: string(s.Status), Channel: string(s.Channel),
			OrderID: s.OrderID, OrderNumber: orderNumberPtr(s.OrderNumber),
			CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, ClosedAt: s.ClosedAt,
		},
		MessagesCount: s.MessagesCount,
		LastMessage:   newMessageOut(s.LastMessage),
	}
}

func requireCustomerID(r *http.Request) (string, error) {
	customerID, ok := auth.CustomerIDFromContext(r.Context())
	if !ok {
		return "", apperr.Unauthorized("unauthenticated", "требуется вход в систему")
	}
	return customerID, nil
}

func createTicketHandler(svc supportService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, err := requireCustomerID(r)
		if err != nil {
			return err
		}
		var req createTicketRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		t, err := svc.Create(r.Context(), support.CreateInput{
			CustomerID: customerID, Name: req.Name, Phone: req.Phone,
			Topic: support.Topic(req.Topic), Message: req.Message, OrderID: req.OrderID,
			Channel: support.ChannelApp,
		})
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusCreated, newTicketOut(t))
	}
}

// parsePaging reads ?page= (≥ 1, default 1) and ?page_size= (1..50,
// default 20).
func parsePaging(r *http.Request) (int, int, error) {
	page, size := 1, support.DefaultPageSize
	q := r.URL.Query()
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, 0, apperr.BadRequest("invalid_page", "некорректный page")
		}
		page = n
	}
	if v := q.Get("page_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > support.MaxPageSize {
			return 0, 0, apperr.BadRequest("invalid_page_size", "page_size должен быть числом от 1 до 50")
		}
		size = n
	}
	return page, size, nil
}

func listTicketsHandler(svc supportService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, err := requireCustomerID(r)
		if err != nil {
			return err
		}
		page, size, err := parsePaging(r)
		if err != nil {
			return err
		}
		list, total, err := svc.ListForCustomer(r.Context(), customerID, page, size)
		if err != nil {
			return err
		}
		items := make([]ticketSummaryOut, len(list))
		for i, s := range list {
			items[i] = newTicketSummaryOut(s)
		}
		return writeJSON(w, http.StatusOK, ticketListResponse{Items: items, Page: page, PageSize: size, Total: total})
	}
}

func getTicketHandler(svc supportService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, err := requireCustomerID(r)
		if err != nil {
			return err
		}
		t, err := svc.GetForCustomer(r.Context(), customerID, r.PathValue("id"))
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, newTicketOut(t))
	}
}

func addTicketMessageHandler(svc supportService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, err := requireCustomerID(r)
		if err != nil {
			return err
		}
		var req ticketMessageRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		t, err := svc.AddCustomerMessage(r.Context(), customerID, r.PathValue("id"), req.Message)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusCreated, newTicketOut(t))
	}
}
