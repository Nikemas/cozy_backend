package notifications

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/push"
	"github.com/Nikemas/cozy_backend/internal/support"
)

func sampleTicket(customerID *string) support.Ticket {
	return support.Ticket{
		ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Number: 42, CustomerID: customerID,
		Name: "Айгерим <b>", Phone: "+996700111222", Topic: support.TopicReturn,
		OrderNumber: "COZY-20261101-003", Channel: support.ChannelApp, Status: support.StatusNew,
		Messages: []support.Message{{Author: support.AuthorCustomer, Body: "Хочу обменять <размер>"}},
	}
}

func flush(t *testing.T, d *Dispatcher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestTicketCreatedSendsEscapedStaffMessage(t *testing.T) {
	staff := &fakeStaff{}
	d := NewDispatcher(Config{Staff: staff, AdminBaseURL: "https://cozy.kg/"})
	d.TicketCreated(sampleTicket(nil))
	flush(t, d)

	if len(staff.msgs) != 1 {
		t.Fatalf("staff messages = %d", len(staff.msgs))
	}
	msg := staff.msgs[0]
	for _, want := range []string{
		"<b>Новое обращение №42</b>", "Тема: Возврат / обмен", "Канал: приложение, без входа",
		"Айгерим &lt;b&gt; +996 700 111 222", "Заказ: COZY-20261101-003", "Хочу обменять &lt;размер&gt;",
		`<a href="https://cozy.kg/admin/tickets/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee">`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}

func TestCustomerFollowUpNotifiesStaff(t *testing.T) {
	staff := &fakeStaff{}
	d := NewDispatcher(Config{Staff: staff})
	cust := "cust-1"
	d.CustomerMessageAdded(sampleTicket(&cust), support.Message{Author: support.AuthorCustomer, Body: "Ау?"})
	flush(t, d)
	if len(staff.msgs) != 1 || !strings.Contains(staff.msgs[0], "Новое сообщение в обращении №42") || !strings.Contains(staff.msgs[0], "Ау?") {
		t.Fatalf("msgs = %v", staff.msgs)
	}
}

func TestStaffReplyPushesInCustomerLanguage(t *testing.T) {
	p := &fakePush{errFor: map[string]error{"bad": push.ErrInvalidToken}}
	tokens := &fakeTokens{tokens: map[string][]string{"cust-1": {"tok", "bad"}}}
	d := NewDispatcher(Config{Push: p, Tokens: tokens, Contacts: fakeContacts{"cust-1": {Lang: i18n.LangKY}}})
	cust := "cust-1"
	d.StaffReplied(sampleTicket(&cust), support.Message{Author: support.AuthorStaff, Body: strings.Repeat("ж", 300)})
	flush(t, d)

	if len(p.sent) != 1 {
		t.Fatalf("pushes = %d", len(p.sent))
	}
	msg := p.sent[0].msg
	if msg.Title != "№42 кайрылууңузга жооп келди" {
		t.Errorf("title = %q", msg.Title)
	}
	if msg.Data["type"] != PushTypeSupportReply || msg.Data["ticket_id"] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("data = %v", msg.Data)
	}
	if n := len([]rune(msg.Body)); n != maxPushReplyText {
		t.Errorf("body runes = %d, want %d", n, maxPushReplyText)
	}
	if len(tokens.deleted) != 1 || tokens.deleted[0] != "bad" {
		t.Errorf("deleted = %v", tokens.deleted)
	}
}

func TestStaffReplyToGuestOrWithoutProvidersIsNoop(t *testing.T) {
	// Zero config: no FCM, no Telegram, no token store — must not panic.
	d := NewDispatcher(Config{})
	d.StaffReplied(sampleTicket(nil), support.Message{Body: "x"})
	cust := "cust-1"
	d.StaffReplied(sampleTicket(&cust), support.Message{Body: "x"})
	d.TicketCreated(sampleTicket(nil))
	flush(t, d)
}
