package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/support"
)

// fakeSupport is a supportService recording the customer each call was
// scoped to. Tickets owned by someone else answer 404, a closed one 409 —
// the same outcomes support.Service produces.
type fakeSupport struct {
	created    support.CreateInput
	listPage   int
	listSize   int
	owner      string
	closed     bool
	lastCaller string
}

var (
	errFakeNotFound = apperr.NotFound("ticket_not_found", "обращение не найдено")
	errFakeClosed   = apperr.Conflict("ticket_closed", "обращение закрыто — создайте новое, если вопрос остался")
)

func fakeTicket(id string) *support.Ticket {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	return &support.Ticket{
		ID: id, Number: 7, Name: "Айгерим", Phone: "+996700111222", Topic: support.TopicOrder,
		Channel: support.ChannelApp, Status: support.StatusNew, CreatedAt: now, UpdatedAt: now,
		Messages: []support.Message{{ID: "m1", Author: support.AuthorCustomer, Body: "где заказ", CreatedAt: now}},
	}
}

func (f *fakeSupport) Create(_ context.Context, in support.CreateInput) (*support.Ticket, error) {
	f.created = in
	return fakeTicket("t1"), nil
}

func (f *fakeSupport) ListForCustomer(_ context.Context, customerID string, page, size int) ([]support.Summary, int, error) {
	f.lastCaller, f.listPage, f.listSize = customerID, page, size
	return []support.Summary{{ID: "t1", Number: 7, Topic: support.TopicOther, Status: support.StatusNew, Channel: support.ChannelWeb, MessagesCount: 1,
		LastMessage: support.Message{Author: support.AuthorCustomer, Body: "привет"}}}, 1, nil
}

func (f *fakeSupport) GetForCustomer(_ context.Context, customerID, id string) (*support.Ticket, error) {
	f.lastCaller = customerID
	if customerID != f.owner {
		return nil, errFakeNotFound
	}
	return fakeTicket(id), nil
}

func (f *fakeSupport) AddCustomerMessage(ctx context.Context, customerID, id, _ string) (*support.Ticket, error) {
	if _, err := f.GetForCustomer(ctx, customerID, id); err != nil {
		return nil, err
	}
	if f.closed {
		return nil, errFakeClosed
	}
	return fakeTicket(id), nil
}

func serveSupport(t *testing.T, h apperr.HandlerFunc, method, target, body, customerID, pathID string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if customerID != "" {
		r = r.WithContext(auth.NewContextWithCustomerID(r.Context(), customerID))
	}
	if pathID != "" {
		r.SetPathValue("id", pathID)
	}
	w := httptest.NewRecorder()
	apperr.Wrap(h)(w, r)
	return w
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct{ Code string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", w.Body.String(), err)
	}
	return body.Code
}

func TestCreateTicketHandlerUsesAppChannelAndCaller(t *testing.T) {
	f := &fakeSupport{}
	w := serveSupport(t, createTicketHandler(f), http.MethodPost, "/api/v1/support/tickets",
		`{"topic":"order","message":"где заказ","order_id":"o1","name":"А","phone":"0700111222"}`, "c1", "")
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if f.created.CustomerID != "c1" || f.created.Channel != support.ChannelApp || f.created.Topic != support.TopicOrder ||
		f.created.OrderID != "o1" || f.created.Message != "где заказ" {
		t.Fatalf("input = %+v", f.created)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "number", "topic", "status", "channel", "order_id", "order_number", "created_at", "updated_at", "closed_at", "name", "phone", "messages"} {
		if _, ok := out[k]; !ok {
			t.Errorf("response lacks %q: %s", k, w.Body)
		}
	}
	msgs, _ := out["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["text"] != "где заказ" || msgs[0].(map[string]any)["author"] != "customer" {
		t.Fatalf("messages = %v", out["messages"])
	}
}

func TestCreateTicketHandlerBadBodyAndNoAuth(t *testing.T) {
	f := &fakeSupport{}
	if w := serveSupport(t, createTicketHandler(f), http.MethodPost, "/api/v1/support/tickets", `{`, "c1", ""); w.Code != http.StatusBadRequest || errCode(t, w) != "bad_request" {
		t.Fatalf("bad json: %d %s", w.Code, w.Body)
	}
	if w := serveSupport(t, createTicketHandler(f), http.MethodPost, "/api/v1/support/tickets", `{}`, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: %d", w.Code)
	}
}

func TestListTicketsHandlerPaging(t *testing.T) {
	f := &fakeSupport{}
	w := serveSupport(t, listTicketsHandler(f), http.MethodGet, "/api/v1/support/tickets?page=2&page_size=5", "", "c1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.lastCaller != "c1" || f.listPage != 2 || f.listSize != 5 {
		t.Fatalf("caller=%s page=%d size=%d", f.lastCaller, f.listPage, f.listSize)
	}
	var out ticketListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 1 || out.Page != 2 || out.PageSize != 5 || len(out.Items) != 1 || out.Items[0].LastMessage.Text != "привет" {
		t.Fatalf("out = %+v", out)
	}

	for _, q := range []string{"page=0", "page=x", "page_size=0", "page_size=51"} {
		w := serveSupport(t, listTicketsHandler(f), http.MethodGet, "/api/v1/support/tickets?"+q, "", "c1", "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, w.Code)
		}
	}
}

func TestGetTicketHandlerForeignIs404(t *testing.T) {
	f := &fakeSupport{owner: "owner"}
	w := serveSupport(t, getTicketHandler(f), http.MethodGet, "/api/v1/support/tickets/t1", "", "intruder", "t1")
	if w.Code != http.StatusNotFound || errCode(t, w) != "ticket_not_found" {
		t.Fatalf("foreign: %d %s", w.Code, w.Body)
	}
	w = serveSupport(t, getTicketHandler(f), http.MethodGet, "/api/v1/support/tickets/t1", "", "owner", "t1")
	if w.Code != http.StatusOK {
		t.Fatalf("own: %d %s", w.Code, w.Body)
	}
}

func TestAddTicketMessageHandlerClosedIs409(t *testing.T) {
	f := &fakeSupport{owner: "c1", closed: true}
	w := serveSupport(t, addTicketMessageHandler(f), http.MethodPost, "/api/v1/support/tickets/t1/messages", `{"message":"ещё"}`, "c1", "t1")
	if w.Code != http.StatusConflict || errCode(t, w) != "ticket_closed" {
		t.Fatalf("closed: %d %s", w.Code, w.Body)
	}
	f.closed = false
	w = serveSupport(t, addTicketMessageHandler(f), http.MethodPost, "/api/v1/support/tickets/t1/messages", `{"message":"ещё"}`, "c1", "t1")
	if w.Code != http.StatusCreated {
		t.Fatalf("open: %d %s", w.Code, w.Body)
	}
}
