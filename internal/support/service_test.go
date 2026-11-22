package support

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// fakeStore is an in-memory Store.
type fakeStore struct {
	mu        sync.Mutex
	customers map[string][2]string // id -> name, phone
	orders    map[string]string    // order id -> customer id
	tickets   map[string]*Ticket
	created   []NewTicket
	seq       int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{customers: map[string][2]string{}, orders: map[string]string{}, tickets: map[string]*Ticket{}}
}

func (f *fakeStore) CustomerContact(_ context.Context, id string) (string, string, bool, error) {
	c, ok := f.customers[id]
	return c[0], c[1], ok, nil
}

func (f *fakeStore) OrderOfCustomer(_ context.Context, orderID, customerID string) (bool, error) {
	return f.orders[orderID] == customerID, nil
}

func (f *fakeStore) Create(_ context.Context, in NewTicket, maxPerHour int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.created {
		if c.Phone == in.Phone || (in.CustomerID != nil && c.CustomerID != nil && *c.CustomerID == *in.CustomerID) {
			n++
		}
	}
	if n >= maxPerHour {
		return "", errTooManyTickets
	}
	f.created = append(f.created, in)
	f.seq++
	id := uuid.NewString()
	now := time.Now()
	f.tickets[id] = &Ticket{
		ID: id, Number: f.seq, CustomerID: in.CustomerID, Name: in.Name, Phone: in.Phone,
		Topic: in.Topic, OrderID: in.OrderID, Channel: in.Channel, Status: StatusNew,
		CreatedAt: now, UpdatedAt: now,
		Messages: []Message{{ID: uuid.NewString(), Author: AuthorCustomer, Body: in.Message, CreatedAt: now}},
	}
	return id, nil
}

func (f *fakeStore) Get(_ context.Context, id string) (*Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tickets[id]
	if !ok {
		return nil, errTicketNotFound
	}
	c := *t
	c.Messages = append([]Message(nil), t.Messages...)
	return &c, nil
}

func (f *fakeStore) ListByCustomer(context.Context, string, int, int) ([]Summary, int, error) {
	return nil, 0, nil
}

func (f *fakeStore) List(_ context.Context, _ ListFilter) ([]Summary, int, error) { return nil, 0, nil }

func (f *fakeStore) AddMessage(_ context.Context, id string, author Author, staffID *string, body string) (Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tickets[id]
	if !ok {
		return Message{}, errTicketNotFound
	}
	if t.Status == StatusClosed {
		return Message{}, errTicketClosed
	}
	m := Message{ID: uuid.NewString(), Author: author, StaffID: staffID, Body: body, CreatedAt: time.Now()}
	t.Messages = append(t.Messages, m)
	if author == AuthorStaff && t.Status == StatusNew {
		t.Status = StatusInProgress
	}
	return m, nil
}

func (f *fakeStore) CustomerMessagesSince(context.Context, string, time.Time) (int, error) {
	return 0, nil
}

func (f *fakeStore) SetStatus(_ context.Context, id string, s Status) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tickets[id]
	if !ok {
		return "", errTicketNotFound
	}
	from := t.Status
	t.Status = s
	return from, nil
}

func (f *fakeStore) CountNew(context.Context) (int, error) { return 0, nil }

// recNotifier records notifier calls.
type recNotifier struct {
	mu                       sync.Mutex
	created, follow, replies int
	lastReply                Message
}

func (r *recNotifier) TicketCreated(Ticket) { r.mu.Lock(); r.created++; r.mu.Unlock() }
func (r *recNotifier) CustomerMessageAdded(Ticket, Message) {
	r.mu.Lock()
	r.follow++
	r.mu.Unlock()
}
func (r *recNotifier) StaffReplied(_ Ticket, m Message) {
	r.mu.Lock()
	r.replies++
	r.lastReply = m
	r.mu.Unlock()
}

const (
	custA = "11111111-1111-1111-1111-111111111111"
	custB = "22222222-2222-2222-2222-222222222222"
)

func newTestService() (*Service, *fakeStore, *recNotifier) {
	st := newFakeStore()
	st.customers[custA] = [2]string{"Айгерим", "+996700111222"}
	st.customers[custB] = [2]string{"", "+996700333444"}
	n := &recNotifier{}
	return NewServiceWithStore(st).WithNotifier(n), st, n
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want apperr code %q", err, code)
	}
}

func TestCreateGuestRequiresNameAndPhone(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()
	base := CreateInput{Topic: TopicOther, Message: "Есть ли 42 размер?", Channel: ChannelWeb}

	in := base
	in.Phone = "0700 123 456"
	_, err := svc.Create(ctx, in)
	wantCode(t, err, "name_required")

	in = base
	in.Name = "Гость"
	in.Phone = "123"
	_, err = svc.Create(ctx, in)
	wantCode(t, err, "invalid_phone")

	in = base
	in.Name, in.Phone, in.OrderID = "Гость", "0700123456", uuid.NewString()
	_, err = svc.Create(ctx, in)
	wantCode(t, err, "invalid_ticket_order")
}

func TestCreateGuestNormalizesPhoneAndNotifies(t *testing.T) {
	svc, _, n := newTestService()
	tk, err := svc.Create(context.Background(), CreateInput{
		Name: "  Гость   Тест ", Phone: "0700 123 456", Topic: TopicDelivery,
		Message: "  Когда доставка?  ", Channel: ChannelWeb,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tk.Phone != "+996700123456" || tk.Name != "Гость Тест" || tk.FirstMessage() != "Когда доставка?" {
		t.Fatalf("ticket = %+v", tk)
	}
	if !tk.IsGuest() || tk.Status != StatusNew {
		t.Fatalf("guest=%v status=%s", tk.IsGuest(), tk.Status)
	}
	if n.created != 1 {
		t.Fatalf("TicketCreated calls = %d, want 1", n.created)
	}
}

func TestCreateCustomerDefaultsFromAccount(t *testing.T) {
	svc, _, _ := newTestService()
	tk, err := svc.Create(context.Background(), CreateInput{
		CustomerID: custA, Topic: TopicPayment, Message: "Не прошла оплата", Channel: ChannelApp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tk.Name != "Айгерим" || tk.Phone != "+996700111222" || tk.CustomerID == nil || *tk.CustomerID != custA {
		t.Fatalf("ticket = %+v", tk)
	}
	if tk.Channel != ChannelApp {
		t.Fatalf("channel = %s", tk.Channel)
	}
}

func TestCreateValidation(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()
	cases := []struct {
		name string
		in   CreateInput
		code string
	}{
		{"bad topic", CreateInput{CustomerID: custA, Topic: "spam", Message: "x", Channel: ChannelApp}, "invalid_ticket_topic"},
		{"bad channel", CreateInput{CustomerID: custA, Topic: TopicOther, Message: "x", Channel: "sms"}, "invalid_ticket_channel"},
		{"blank message", CreateInput{CustomerID: custA, Topic: TopicOther, Message: "  \n ", Channel: ChannelApp}, "message_required"},
		{"long message", CreateInput{CustomerID: custA, Topic: TopicOther, Message: strings.Repeat("я", MaxMessageLen+1), Channel: ChannelApp}, "message_too_long"},
		{"long name", CreateInput{CustomerID: custA, Name: strings.Repeat("a", MaxNameLen+1), Topic: TopicOther, Message: "x", Channel: ChannelApp}, "name_too_long"},
		{"bad phone", CreateInput{CustomerID: custA, Phone: "+7 900", Topic: TopicOther, Message: "x", Channel: ChannelApp}, "invalid_phone"},
		{"bad order id", CreateInput{CustomerID: custA, OrderID: "nope", Topic: TopicOrder, Message: "x", Channel: ChannelApp}, "invalid_ticket_order"},
		{"unknown customer", CreateInput{CustomerID: uuid.NewString(), Topic: TopicOther, Message: "x", Channel: ChannelApp}, "customer_not_found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.Create(ctx, c.in)
			wantCode(t, err, c.code)
		})
	}
	// Exactly MaxMessageLen runes is fine.
	if _, err := svc.Create(ctx, CreateInput{CustomerID: custA, Topic: TopicOther, Message: strings.Repeat("я", MaxMessageLen), Channel: ChannelApp}); err != nil {
		t.Fatalf("max-length message rejected: %v", err)
	}
}

func TestCreateOrderMustBelongToCustomer(t *testing.T) {
	svc, st, _ := newTestService()
	own, foreign := uuid.NewString(), uuid.NewString()
	st.orders[own] = custA
	st.orders[foreign] = custB
	ctx := context.Background()

	_, err := svc.Create(ctx, CreateInput{CustomerID: custA, OrderID: foreign, Topic: TopicOrder, Message: "где заказ", Channel: ChannelApp})
	wantCode(t, err, "invalid_ticket_order")

	tk, err := svc.Create(ctx, CreateInput{CustomerID: custA, OrderID: own, Topic: TopicOrder, Message: "где заказ", Channel: ChannelApp})
	if err != nil {
		t.Fatal(err)
	}
	if tk.OrderID == nil || *tk.OrderID != own {
		t.Fatalf("order_id = %v", tk.OrderID)
	}
}

func TestCreateRateLimited(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()
	for i := 0; i < MaxTicketsPerHour; i++ {
		if _, err := svc.Create(ctx, CreateInput{CustomerID: custA, Topic: TopicOther, Message: "x", Channel: ChannelApp}); err != nil {
			t.Fatalf("ticket %d: %v", i+1, err)
		}
	}
	_, err := svc.Create(ctx, CreateInput{CustomerID: custA, Topic: TopicOther, Message: "x", Channel: ChannelApp})
	wantCode(t, err, "too_many_tickets")
}

func TestForeignTicketIsNotFound(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()
	tk, err := svc.Create(ctx, CreateInput{CustomerID: custA, Topic: TopicOther, Message: "x", Channel: ChannelApp})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.GetForCustomer(ctx, custB, tk.ID)
	wantCode(t, err, "ticket_not_found")
	_, err = svc.AddCustomerMessage(ctx, custB, tk.ID, "чужое")
	wantCode(t, err, "ticket_not_found")
	_, err = svc.GetForCustomer(ctx, custA, "not-a-uuid")
	wantCode(t, err, "ticket_not_found")
	if _, err := svc.GetForCustomer(ctx, custA, tk.ID); err != nil {
		t.Fatalf("own ticket: %v", err)
	}
}

func TestGuestTicketNotVisibleToCustomer(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()
	tk, err := svc.Create(ctx, CreateInput{Name: "Гость", Phone: "+996700111222", Topic: TopicOther, Message: "x", Channel: ChannelWeb})
	if err != nil {
		t.Fatal(err)
	}
	// Same phone as custA, but a guest request is not linked to the account.
	_, err = svc.GetForCustomer(ctx, custA, tk.ID)
	wantCode(t, err, "ticket_not_found")
}

func TestReplyFlowAndClosedTicket(t *testing.T) {
	svc, _, n := newTestService()
	ctx := context.Background()
	tk, err := svc.Create(ctx, CreateInput{CustomerID: custA, Topic: TopicReturn, Message: "хочу обменять", Channel: ChannelApp})
	if err != nil {
		t.Fatal(err)
	}

	tk, err = svc.AddCustomerMessage(ctx, custA, tk.ID, "размер 41")
	if err != nil {
		t.Fatal(err)
	}
	if len(tk.Messages) != 2 || n.follow != 1 {
		t.Fatalf("messages=%d follow-ups=%d", len(tk.Messages), n.follow)
	}

	tk, err = svc.Reply(ctx, tk.ID, "", "Привезите в магазин")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Status != StatusInProgress || n.replies != 1 || n.lastReply.Author != AuthorStaff {
		t.Fatalf("status=%s replies=%d last=%+v", tk.Status, n.replies, n.lastReply)
	}

	tk, from, err := svc.SetStatus(ctx, tk.ID, StatusClosed)
	if err != nil || from != StatusInProgress || tk.Status != StatusClosed {
		t.Fatalf("SetStatus: from=%s status=%v err=%v", from, tk, err)
	}
	_, err = svc.AddCustomerMessage(ctx, custA, tk.ID, "ещё вопрос")
	wantCode(t, err, "ticket_closed")
	_, err = svc.Reply(ctx, tk.ID, "", "ответ")
	wantCode(t, err, "ticket_closed")

	_, _, err = svc.SetStatus(ctx, tk.ID, "done")
	wantCode(t, err, "invalid_ticket_status")
}

func TestListRejectsUnknownFilters(t *testing.T) {
	svc, _, _ := newTestService()
	_, _, err := svc.List(context.Background(), ListFilter{Status: "weird"})
	wantCode(t, err, "invalid_ticket_status")
	_, _, err = svc.List(context.Background(), ListFilter{Topic: "weird"})
	wantCode(t, err, "invalid_ticket_topic")
}

func TestNormalizePage(t *testing.T) {
	cases := []struct{ p, s, wp, ws int }{
		{0, 0, 1, DefaultPageSize},
		{3, 10, 3, 10},
		{-1, 500, 1, MaxPageSize},
	}
	for _, c := range cases {
		if p, s := NormalizePage(c.p, c.s); p != c.wp || s != c.ws {
			t.Errorf("NormalizePage(%d,%d) = %d,%d want %d,%d", c.p, c.s, p, s, c.wp, c.ws)
		}
	}
}

func TestPanickingNotifierDoesNotFailCreate(t *testing.T) {
	st := newFakeStore()
	svc := NewServiceWithStore(st).WithNotifier(panicNotifier{})
	if _, err := svc.Create(context.Background(), CreateInput{Name: "Г", Phone: "0700123456", Topic: TopicOther, Message: "x", Channel: ChannelWeb}); err != nil {
		t.Fatal(err)
	}
}

type panicNotifier struct{}

func (panicNotifier) TicketCreated(Ticket)                 { panic("boom") }
func (panicNotifier) CustomerMessageAdded(Ticket, Message) { panic("boom") }
func (panicNotifier) StaffReplied(Ticket, Message)         { panic("boom") }
