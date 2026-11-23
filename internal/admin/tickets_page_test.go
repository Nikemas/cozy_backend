package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/staff"
	"github.com/Nikemas/cozy_backend/internal/support"
)

const testTicketID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

type fakeTickets struct {
	ticket     *support.Ticket
	filter     support.ListFilter
	repliedBy  string
	replyBody  string
	replyErr   error
	newStatus  support.Status
	countNew   int
	countCalls int
}

func (f *fakeTickets) List(_ context.Context, fl support.ListFilter) ([]support.Summary, int, error) {
	f.filter = fl
	cust := "c1"
	return []support.Summary{{
		ID: testTicketID, Number: 12, CustomerID: &cust, Name: "Айгерим", Phone: "+996700111222",
		Topic: support.TopicReturn, Channel: support.ChannelApp, Status: support.StatusNew,
		CreatedAt:   time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC),
		LastMessage: support.Message{Author: support.AuthorCustomer, Body: "Хочу обменять"},
	}, {
		ID: "ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee", Number: 13, Name: "Гость", Phone: "+996555000111",
		Topic: support.TopicOther, Channel: support.ChannelWeb, Status: support.StatusClosed,
		LastMessage: support.Message{Author: support.AuthorStaff, Body: "Готово"},
	}}, 2, nil
}

func (f *fakeTickets) Get(_ context.Context, id string) (*support.Ticket, error) {
	if f.ticket == nil || id != f.ticket.ID {
		return nil, apperr.NotFound("ticket_not_found", "обращение не найдено")
	}
	return f.ticket, nil
}

func (f *fakeTickets) Reply(_ context.Context, id, staffID, body string) (*support.Ticket, error) {
	if f.replyErr != nil {
		return nil, f.replyErr
	}
	f.repliedBy, f.replyBody = staffID, body
	return f.Get(context.Background(), id)
}

func (f *fakeTickets) SetStatus(_ context.Context, id string, s support.Status) (*support.Ticket, support.Status, error) {
	t, err := f.Get(context.Background(), id)
	if err != nil {
		return nil, "", err
	}
	from := t.Status
	f.newStatus = s
	c := *t
	c.Status = s
	return &c, from, nil
}

func (f *fakeTickets) CountNew(context.Context) (int, error) {
	f.countCalls++
	return f.countNew, nil
}

func sampleAdminTicket() *support.Ticket {
	order := "11111111-2222-3333-4444-555555555555"
	cust := "c1"
	staffID := "s1"
	return &support.Ticket{
		ID: testTicketID, Number: 12, CustomerID: &cust, Name: "Айгерим", Phone: "+996700111222",
		Topic: support.TopicReturn, OrderID: &order, OrderNumber: "COZY-20261101-003",
		Channel: support.ChannelApp, Status: support.StatusNew,
		Messages: []support.Message{
			{Author: support.AuthorCustomer, Body: "Хочу обменять <42>"},
			{Author: support.AuthorStaff, StaffID: &staffID, StaffName: "Менеджер Айбек", Body: "Привезите в магазин"},
		},
	}
}

func ticketsHandlers(t *testing.T, f *fakeTickets) *handlers {
	t.Helper()
	return &handlers{render: newTestRenderer(t), tickets: f}
}

func ticketReq(method, target string, form url.Values, role staff.Role) *http.Request {
	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	st := &staff.Staff{ID: "s1", Name: "Менеджер Айбек", Role: role}
	return r.WithContext(staff.NewContextWithStaff(r.Context(), st))
}

func TestTicketsListRendersRowsAndFilters(t *testing.T) {
	f := &fakeTickets{}
	h := ticketsHandlers(t, f)
	w := httptest.NewRecorder()
	h.ticketsListPage(w, ticketReq(http.MethodGet, "/admin/tickets?status=new&topic=return&q=0700&page=2", nil, staff.RoleManager))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if f.filter.Status != support.StatusNew || f.filter.Topic != support.TopicReturn || f.filter.Query != "0700" || f.filter.Page != 2 {
		t.Fatalf("filter = %+v", f.filter)
	}
	body := w.Body.String()
	for _, want := range []string{"№12", "Айгерим", "996 700 111 222", "Возврат / обмен", "Хочу обменять", "без входа", "admin-chip--new", "/admin/tickets/" + testTicketID} {
		if !strings.Contains(body, want) {
			t.Errorf("list lacks %q", want)
		}
	}
}

func TestTicketsListDropsUnknownFilters(t *testing.T) {
	f := &fakeTickets{}
	h := ticketsHandlers(t, f)
	h.ticketsListPage(httptest.NewRecorder(), ticketReq(http.MethodGet, "/admin/tickets?status=bogus&topic=spam", nil, staff.RoleOwner))
	if f.filter.Status != "" || f.filter.Topic != "" {
		t.Fatalf("filter = %+v", f.filter)
	}
}

func TestTicketDetailShowsContactsOrderAndConversation(t *testing.T) {
	f := &fakeTickets{ticket: sampleAdminTicket()}
	h := ticketsHandlers(t, f)
	r := ticketReq(http.MethodGet, "/admin/tickets/"+testTicketID, nil, staff.RoleOwner)
	r.SetPathValue("id", testTicketID)
	w := httptest.NewRecorder()
	h.ticketDetailPage(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{
		"Обращение №12", `href="tel:&#43;996700111222"`, `href="https://wa.me/996700111222"`,
		`href="/admin/orders/11111111-2222-3333-4444-555555555555"`, "COZY-20261101-003",
		"Хочу обменять &lt;42&gt;", "Менеджер Айбек", `action="/admin/tickets/` + testTicketID + `/reply"`,
		`name="status" value="in_progress"`, `name="status" value="closed"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	if strings.Contains(body, `name="status" value="new"`) {
		t.Error("current status offered as a button")
	}
}

func TestTicketDetailClosedHidesReplyForm(t *testing.T) {
	tk := sampleAdminTicket()
	tk.Status = support.StatusClosed
	h := ticketsHandlers(t, &fakeTickets{ticket: tk})
	r := ticketReq(http.MethodGet, "/admin/tickets/"+testTicketID, nil, staff.RoleOwner)
	r.SetPathValue("id", testTicketID)
	w := httptest.NewRecorder()
	h.ticketDetailPage(w, r)
	if strings.Contains(w.Body.String(), "/reply") || !strings.Contains(w.Body.String(), "верните его в работу") {
		t.Fatal("closed ticket should show the reopen hint instead of the reply form")
	}
}

func TestTicketDetailUnknownIs404(t *testing.T) {
	h := ticketsHandlers(t, &fakeTickets{})
	r := ticketReq(http.MethodGet, "/admin/tickets/x", nil, staff.RoleOwner)
	r.SetPathValue("id", "x")
	w := httptest.NewRecorder()
	h.ticketDetailPage(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d", w.Code)
	}
}

func TestTicketReplyRecordsStaffAndRedirects(t *testing.T) {
	f := &fakeTickets{ticket: sampleAdminTicket()}
	h := ticketsHandlers(t, f)
	r := ticketReq(http.MethodPost, "/admin/tickets/"+testTicketID+"/reply", url.Values{"message": {"Ждём вас"}}, staff.RoleManager)
	r.SetPathValue("id", testTicketID)
	w := httptest.NewRecorder()
	h.ticketReply(w, r)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "toast=ticket_replied") {
		t.Fatalf("status=%d location=%q", w.Code, w.Header().Get("Location"))
	}
	if f.repliedBy != "s1" || f.replyBody != "Ждём вас" {
		t.Fatalf("reply by %q body %q", f.repliedBy, f.replyBody)
	}

	f.replyErr = apperr.Conflict("ticket_closed", "обращение закрыто — создайте новое, если вопрос остался")
	w = httptest.NewRecorder()
	h.ticketReply(w, r)
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "toast_code=ticket_closed") {
		t.Fatalf("closed reply location = %q", loc)
	}
}

func TestTicketStatusUpdate(t *testing.T) {
	f := &fakeTickets{ticket: sampleAdminTicket()}
	h := ticketsHandlers(t, f)
	r := ticketReq(http.MethodPost, "/admin/tickets/"+testTicketID+"/status", url.Values{"status": {"closed"}}, staff.RoleOwner)
	r.SetPathValue("id", testTicketID)
	w := httptest.NewRecorder()
	h.ticketStatusUpdate(w, r)
	if f.newStatus != support.StatusClosed || !strings.Contains(w.Header().Get("Location"), "toast=ticket_status_closed") {
		t.Fatalf("status=%s location=%q", f.newStatus, w.Header().Get("Location"))
	}
}

func TestTicketStatusAuditEntry(t *testing.T) {
	tk := sampleAdminTicket()
	tk.Status = support.StatusClosed
	e := ticketStatusAuditEntry(tk, support.StatusNew)
	if e.EntityID != testTicketID || e.Summary != "Обращение №12" || e.Details["from"] != "new" || e.Details["to"] != "closed" {
		t.Fatalf("entry = %+v", e)
	}
}

func TestTicketBadgeOnlyForRolesThatSeeTickets(t *testing.T) {
	f := &fakeTickets{countNew: 3}
	mw := withTicketBadge(f)
	var got int
	next := func(w http.ResponseWriter, _ *http.Request) { got = newTicketsFromWriter(w) }

	mw(next)(httptest.NewRecorder(), ticketReq(http.MethodGet, "/admin/orders", nil, staff.RoleManager))
	if got != 3 {
		t.Fatalf("manager badge = %d", got)
	}
	got = -1
	mw(next)(httptest.NewRecorder(), ticketReq(http.MethodGet, "/admin/orders", nil, staff.RolePointStaff))
	if got != 0 || f.countCalls != 1 {
		t.Fatalf("point_staff badge = %d, counts = %d", got, f.countCalls)
	}
}

func TestPointStaffCannotSeeTickets(t *testing.T) {
	if roleSeesNav("tickets", staff.RolePointStaff) || !roleSeesNav("tickets", staff.RoleOwner) || !roleSeesNav("tickets", staff.RoleManager) {
		t.Fatal("tickets must be owner+manager only")
	}
	if roleSeesNav("tickets", staff.Role("storekeeper")) {
		t.Fatal("other roles must not see tickets")
	}
}
