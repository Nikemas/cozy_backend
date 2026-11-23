//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// apiCall sends a JSON request with a bearer token.
func (a *testApp) apiCall(t *testing.T, method, path, access, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Authorization", "Bearer "+access)
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, r)
	return w
}

func customerAccess(t *testing.T, a *testApp, customerID string) string {
	t.Helper()
	return a.customerSession(t, customerID).Value
}

// insertOrder creates a minimal delivered order for customerID.
func insertOrder(t *testing.T, f fixture, customerID string) (id, number string) {
	t.Helper()
	number = "COZY-IT-" + uuid.NewString()[:8]
	err := testDB.QueryRowContext(ctxT(t), `
		INSERT INTO orders (order_number, customer_id, address_id, point_id, status, payment_method, total_amount)
		VALUES ($1, $2, $3, $4, 'delivered', 'cash_on_delivery', 2500) RETURNING id`,
		number, customerID, f.AddressID, f.PointA).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id, number
}

type apiTicket struct {
	ID          string  `json:"id"`
	Number      int64   `json:"number"`
	Topic       string  `json:"topic"`
	Status      string  `json:"status"`
	Channel     string  `json:"channel"`
	OrderID     *string `json:"order_id"`
	OrderNumber *string `json:"order_number"`
	Name        string  `json:"name"`
	Phone       string  `json:"phone"`
	Messages    []struct {
		Author string `json:"author"`
		Text   string `json:"text"`
	} `json:"messages"`
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct{ Code string }
	decodeJSON(t, w, &e)
	return e.Code
}

func TestSupportAPIFlow(t *testing.T) {
	a := app(t)
	f := newFixture(t, 1, 1)
	other := newCustomer(t)
	orderID, orderNumber := insertOrder(t, f, f.CustomerID)
	foreignOrder, _ := insertOrder(t, f, other)
	access := customerAccess(t, a, f.CustomerID)
	otherAccess := customerAccess(t, a, other)

	// Someone else's order can't be attached.
	w := a.apiCall(t, http.MethodPost, "/api/v1/support/tickets", access,
		`{"topic":"order","message":"где заказ?","order_id":"`+foreignOrder+`"}`)
	wantStatus(t, w, http.StatusBadRequest)
	if c := errorCode(t, w); c != "invalid_ticket_order" {
		t.Fatalf("code = %s", c)
	}

	w = a.apiCall(t, http.MethodPost, "/api/v1/support/tickets", access,
		`{"topic":"order","message":"  Где мой заказ?  ","order_id":"`+orderID+`"}`)
	wantStatus(t, w, http.StatusCreated)
	var tk apiTicket
	decodeJSON(t, w, &tk)
	if tk.Channel != "app" || tk.Status != "new" || tk.Name != "Тест" || !strings.HasPrefix(tk.Phone, "+996") ||
		tk.OrderNumber == nil || *tk.OrderNumber != orderNumber || len(tk.Messages) != 1 || tk.Messages[0].Text != "Где мой заказ?" {
		t.Fatalf("ticket = %+v", tk)
	}

	// List.
	w = a.apiCall(t, http.MethodGet, "/api/v1/support/tickets?page_size=5", access, "")
	wantStatus(t, w, http.StatusOK)
	var list struct {
		Items []struct {
			ID          string                `json:"id"`
			LastMessage struct{ Text string } `json:"last_message"`
		} `json:"items"`
		Total    int `json:"total"`
		PageSize int `json:"page_size"`
	}
	decodeJSON(t, w, &list)
	if list.Total != 1 || list.PageSize != 5 || len(list.Items) != 1 || list.Items[0].ID != tk.ID {
		t.Fatalf("list = %+v", list)
	}

	// Another customer: 404 on read and write, empty list.
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/support/tickets/" + tk.ID, ""},
		{http.MethodPost, "/api/v1/support/tickets/" + tk.ID + "/messages", `{"message":"чужое"}`},
		{http.MethodGet, "/api/v1/support/tickets/not-a-uuid", ""},
	} {
		w := a.apiCall(t, c.method, c.path, otherAccess, c.body)
		wantStatus(t, w, http.StatusNotFound)
		if code := errorCode(t, w); code != "ticket_not_found" {
			t.Fatalf("%s %s: code %s", c.method, c.path, code)
		}
	}
	w = a.apiCall(t, http.MethodGet, "/api/v1/support/tickets", otherAccess, "")
	decodeJSON(t, w, &list)
	if list.Total != 0 {
		t.Fatalf("other customer sees %d tickets", list.Total)
	}

	// Follow-up.
	w = a.apiCall(t, http.MethodPost, "/api/v1/support/tickets/"+tk.ID+"/messages", access, `{"message":"Номер выше"}`)
	wantStatus(t, w, http.StatusCreated)
	decodeJSON(t, w, &tk)
	if len(tk.Messages) != 2 {
		t.Fatalf("messages = %d", len(tk.Messages))
	}

	// Close in the DB the way staff would; the customer can't write any more.
	if _, err := testDB.ExecContext(ctxT(t), `UPDATE support_tickets SET status = 'closed', closed_at = now() WHERE id = $1`, tk.ID); err != nil {
		t.Fatal(err)
	}
	w = a.apiCall(t, http.MethodPost, "/api/v1/support/tickets/"+tk.ID+"/messages", access, `{"message":"ещё"}`)
	wantStatus(t, w, http.StatusConflict)
	if c := errorCode(t, w); c != "ticket_closed" {
		t.Fatalf("code = %s", c)
	}

	// Validation and auth.
	w = a.apiCall(t, http.MethodPost, "/api/v1/support/tickets", access, `{"topic":"spam","message":"x"}`)
	wantStatus(t, w, http.StatusBadRequest)
	w = a.apiCall(t, http.MethodGet, "/api/v1/support/tickets", "bad-token", "")
	wantStatus(t, w, http.StatusUnauthorized)
}

func TestSupportAPIRateLimit(t *testing.T) {
	a := app(t)
	customer := newCustomer(t)
	access := customerAccess(t, a, customer)
	for i := 0; i < 5; i++ {
		w := a.apiCall(t, http.MethodPost, "/api/v1/support/tickets", access, `{"topic":"other","message":"вопрос"}`)
		wantStatus(t, w, http.StatusCreated)
	}
	w := a.apiCall(t, http.MethodPost, "/api/v1/support/tickets", access, `{"topic":"other","message":"вопрос"}`)
	wantStatus(t, w, http.StatusTooManyRequests)
	if c := errorCode(t, w); c != "too_many_tickets" {
		t.Fatalf("code = %s", c)
	}
}

func TestSupportWebGuestAndCustomer(t *testing.T) {
	a := app(t)
	f := newFixture(t, 1, 1)
	phone := uniquePhone()

	// Guest: name and phone required.
	w := a.do(t, req{method: http.MethodPost, path: "/support", form: url.Values{
		"topic": {"delivery"}, "message": {"Когда привезут?"}, "phone": {phone},
	}})
	wantStatus(t, w, http.StatusBadRequest)
	wantBody(t, w, "укажите имя")

	w = a.do(t, req{method: http.MethodPost, path: "/support", form: url.Values{
		"name": {"Гость"}, "topic": {"delivery"}, "message": {"Когда привезут?"}, "phone": {phone},
	}})
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/support?sent=") {
		t.Fatalf("guest submit: %d %q", w.Code, w.Header().Get("Location"))
	}
	var guestCustomer *string
	var channel string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT customer_id, channel FROM support_tickets WHERE phone = $1`, phone).
		Scan(&guestCustomer, &channel); err != nil {
		t.Fatal(err)
	}
	if guestCustomer != nil || channel != "web" {
		t.Fatalf("guest ticket customer=%v channel=%s", guestCustomer, channel)
	}

	// Staff find the guest by the phone in local format.
	owner, _ := a.newStaffSession(t, staff.RoleOwner, nil)
	w = a.get(t, "/admin/tickets?q="+url.QueryEscape("0"+phone[4:7]+" "+phone[7:]), owner)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, "Когда привезут?")

	// Logged in: prefilled form, own order offered, request page.
	cookie := a.customerSession(t, f.CustomerID)
	orderID, orderNumber := insertOrder(t, f, f.CustomerID)
	w = a.get(t, "/support", cookie)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, `name="order_id"`, orderNumber, `value="Тест"`)

	w = a.do(t, req{method: http.MethodPost, path: "/support", cookies: []*http.Cookie{cookie}, form: url.Values{
		"topic": {"return"}, "message": {"Хочу обменять"}, "order_id": {orderID},
	}})
	wantStatus(t, w, http.StatusSeeOther)
	ticketPath := w.Header().Get("Location")
	if !strings.HasPrefix(ticketPath, "/support/") {
		t.Fatalf("location = %q", ticketPath)
	}
	w = a.get(t, ticketPath, cookie)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, "Хочу обменять", orderNumber, "/messages")

	w = a.do(t, req{method: http.MethodPost, path: ticketPath + "/messages", cookies: []*http.Cookie{cookie}, form: url.Values{"message": {"Размер 42"}}})
	wantStatus(t, w, http.StatusSeeOther)
	w = a.get(t, "/support", cookie)
	wantBody(t, w, "Размер 42", ticketPath)

	// Someone else's request page is the 404 page.
	otherCookie := a.customerSession(t, newCustomer(t))
	w = a.get(t, ticketPath, otherCookie)
	wantStatus(t, w, http.StatusNotFound)
}

func TestSupportAdminFlow(t *testing.T) {
	a := app(t)
	f := newFixture(t, 1, 1)
	access := customerAccess(t, a, f.CustomerID)
	w := a.apiCall(t, http.MethodPost, "/api/v1/support/tickets", access, `{"topic":"payment","message":"Не прошла оплата"}`)
	wantStatus(t, w, http.StatusCreated)
	var tk apiTicket
	decodeJSON(t, w, &tk)

	manager, managerID := a.newStaffSession(t, staff.RoleManager, nil)
	w = a.get(t, "/admin/tickets?status=new", manager)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, "/admin/tickets/"+tk.ID, "Не прошла оплата", `id="admin-tickets-badge"`)

	w = a.get(t, "/admin/tickets/"+tk.ID, manager)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, "wa.me/", "Не прошла оплата")

	// point_staff has no access.
	ps, _ := a.newStaffSession(t, staff.RolePointStaff, &f.PointA)
	if w := a.get(t, "/admin/tickets/"+tk.ID, ps); w.Code == http.StatusOK {
		t.Fatalf("point_staff got the ticket page")
	}

	// Reply → in_progress, visible to the customer as a staff message.
	w = a.do(t, req{method: http.MethodPost, path: "/admin/tickets/" + tk.ID + "/reply", cookies: []*http.Cookie{manager},
		form: url.Values{"message": {"Деньги вернутся в течение 3 дней"}}})
	wantStatus(t, w, http.StatusSeeOther)
	w = a.apiCall(t, http.MethodGet, "/api/v1/support/tickets/"+tk.ID, access, "")
	decodeJSON(t, w, &tk)
	if tk.Status != "in_progress" || len(tk.Messages) != 2 || tk.Messages[1].Author != "staff" {
		t.Fatalf("after reply: %+v", tk)
	}
	var staffID string
	if err := testDB.QueryRowContext(ctxT(t),
		`SELECT staff_id FROM support_ticket_messages WHERE ticket_id = $1 AND author = 'staff'`, tk.ID).Scan(&staffID); err != nil || staffID != managerID {
		t.Fatalf("reply staff_id = %q (%v), want %q", staffID, err, managerID)
	}

	// Close → audit row, customer can't follow up; reopen works.
	w = a.do(t, req{method: http.MethodPost, path: "/admin/tickets/" + tk.ID + "/status", cookies: []*http.Cookie{manager},
		form: url.Values{"status": {"closed"}}})
	wantStatus(t, w, http.StatusSeeOther)
	var n int
	if err := testDB.QueryRowContext(ctxT(t),
		`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'ticket' AND entity_id = $1 AND action = 'ticket.status' AND staff_id = $2`,
		tk.ID, managerID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows = %d (%v)", n, err)
	}
	w = a.apiCall(t, http.MethodPost, "/api/v1/support/tickets/"+tk.ID+"/messages", access, `{"message":"ещё"}`)
	wantStatus(t, w, http.StatusConflict)
	w = a.do(t, req{method: http.MethodPost, path: "/admin/tickets/" + tk.ID + "/reply", cookies: []*http.Cookie{manager},
		form: url.Values{"message": {"x"}}})
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "toast_code=ticket_closed") {
		t.Fatalf("reply to closed: %q", loc)
	}
	var closedAt *string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT closed_at::text FROM support_tickets WHERE id = $1`, tk.ID).Scan(&closedAt); err != nil || closedAt == nil {
		t.Fatalf("closed_at = %v (%v)", closedAt, err)
	}
	w = a.do(t, req{method: http.MethodPost, path: "/admin/tickets/" + tk.ID + "/status", cookies: []*http.Cookie{manager},
		form: url.Values{"status": {"in_progress"}}})
	wantStatus(t, w, http.StatusSeeOther)

	// The journal page shows it.
	owner, _ := a.newStaffSession(t, staff.RoleOwner, nil)
	w = a.get(t, "/admin/audit?entity=ticket", owner)
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, "Обращение №")
}

func TestAccountDeletionRemovesSupportTickets(t *testing.T) {
	a := app(t)
	customer := newCustomer(t)
	access := customerAccess(t, a, customer)
	w := a.apiCall(t, http.MethodPost, "/api/v1/support/tickets", access, `{"topic":"other","message":"удалите меня"}`)
	wantStatus(t, w, http.StatusCreated)
	if err := a.auth.DeleteCustomer(ctxT(t), customer); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := testDB.QueryRowContext(ctxT(t), `SELECT COUNT(*) FROM support_tickets WHERE customer_id = $1`, customer).Scan(&n); err != nil || n != 0 {
		t.Fatalf("tickets left = %d (%v)", n, err)
	}
}
