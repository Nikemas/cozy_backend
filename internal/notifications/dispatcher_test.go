package notifications

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/push"
)

type sentPush struct {
	token string
	msg   push.Message
}

type fakePush struct {
	mu      sync.Mutex
	sent    []sentPush
	errFor  map[string]error
	block   chan struct{} // if non-nil, Send waits on it
	started chan struct{}
}

func (f *fakePush) Send(ctx context.Context, token string, msg push.Message) error {
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errFor[token]; err != nil {
		return err
	}
	f.sent = append(f.sent, sentPush{token, msg})
	return nil
}

type fakeTokens struct {
	mu      sync.Mutex
	tokens  map[string][]string
	deleted []string
	err     error
}

func (f *fakeTokens) TokensForCustomer(_ context.Context, customerID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokens[customerID], f.err
}

func (f *fakeTokens) DeleteToken(_ context.Context, tok string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, tok)
	return nil
}

type fakeStaff struct {
	mu   sync.Mutex
	msgs []string
	err  error
}

func (f *fakeStaff) SendStaffMessage(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, text)
	return f.err
}

type fakeLookup struct {
	info OrderInfo
	err  error
}

func (f fakeLookup) OrderInfo(context.Context, orders.Order) (OrderInfo, error) { return f.info, f.err }

func strPtr(s string) *string { return &s }

func deliveryOrder(status orders.OrderStatus) orders.Order {
	return orders.Order{
		ID: "11111111-2222-3333-4444-555555555555", OrderNumber: "COZY-20260923-007",
		CustomerID: "cust-1", AddressID: strPtr("addr-1"), PointID: strPtr("point-1"),
		Status: status, PaymentMethod: orders.PaymentCashOnDelivery, TotalAmount: 12500,
		Items: []orders.OrderItem{
			{ProductNameSnapshot: "Air <Max>", SizeSnapshot: "42", ColorSnapshot: "Черный", Quantity: 2, Price: 5000},
			{ProductNameSnapshot: "Носки", Quantity: 1, Price: 2500},
		},
	}
}

func shutdown(t *testing.T, d *Dispatcher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestStatusChangePushesToEveryDeviceAndDropsInvalidTokens(t *testing.T) {
	p := &fakePush{errFor: map[string]error{
		"dead":  fmt.Errorf("wrapped: %w", push.ErrInvalidToken),
		"flaky": errors.New("FCM 503"),
	}}
	tokens := &fakeTokens{tokens: map[string][]string{"cust-1": {"good", "dead", "flaky"}}}
	d := NewDispatcher(Config{Push: p, Tokens: tokens})

	d.OrderStatusChanged(deliveryOrder(orders.StatusConfirmed), orders.StatusPlaced)
	shutdown(t, d)

	if len(p.sent) != 1 || p.sent[0].token != "good" {
		t.Fatalf("sent = %+v, want one push to 'good'", p.sent)
	}
	m := p.sent[0].msg
	if m.Title != "Заказ COZY-20260923-007 подтверждён" || m.Body == "" {
		t.Errorf("title/body = %q / %q", m.Title, m.Body)
	}
	wantData := map[string]string{"type": "order_status", "order_id": "11111111-2222-3333-4444-555555555555", "status": "confirmed"}
	if len(m.Data) != len(wantData) {
		t.Errorf("data = %v, want exactly %v", m.Data, wantData)
	}
	for k, v := range wantData {
		if m.Data[k] != v {
			t.Errorf("data[%q] = %q, want %q", k, m.Data[k], v)
		}
	}
	if m.AndroidChannelID != "orders" {
		t.Errorf("AndroidChannelID = %q, want orders", m.AndroidChannelID)
	}
	if len(tokens.deleted) != 1 || tokens.deleted[0] != "dead" {
		t.Errorf("deleted = %v, want only the invalid token (not the transiently failing one)", tokens.deleted)
	}
}

func TestStatusChangeUsesCustomerLanguage(t *testing.T) {
	p := &fakePush{}
	d := NewDispatcher(Config{
		Push:     p,
		Tokens:   &fakeTokens{tokens: map[string][]string{"cust-1": {"t"}}},
		Language: func(context.Context, string) string { return i18n.LangKY },
	})
	d.OrderStatusChanged(deliveryOrder(orders.StatusCancelled), orders.StatusConfirmed)
	shutdown(t, d)

	if len(p.sent) != 1 || p.sent[0].msg.Title != "COZY-20260923-007 буйрутмаңыз жокко чыгарылды" {
		t.Fatalf("sent = %+v, want Kyrgyz cancelled title", p.sent)
	}
}

func TestPickupOrderGetsPickupWording(t *testing.T) {
	o := deliveryOrder(orders.StatusCourierAssigned)
	o.AddressID = nil
	title, _, ok := statusPushText(i18n.LangRU, o)
	if !ok || title != "Заказ COZY-20260923-007 готов к выдаче" {
		t.Fatalf("title = %q ok=%v", title, ok)
	}
}

func TestNoPushForPlacedStatusOrWithoutTokens(t *testing.T) {
	if _, _, ok := statusPushText(i18n.LangRU, deliveryOrder(orders.StatusPlaced)); ok {
		t.Error("placed must not produce a customer push")
	}
	p := &fakePush{}
	d := NewDispatcher(Config{Push: p, Tokens: &fakeTokens{}})
	d.OrderStatusChanged(deliveryOrder(orders.StatusDelivered), orders.StatusCourierAssigned)
	shutdown(t, d)
	if len(p.sent) != 0 {
		t.Errorf("sent %d pushes to a customer with no devices", len(p.sent))
	}
}

func TestOrderCreatedSendsStaffMessage(t *testing.T) {
	staff := &fakeStaff{}
	d := NewDispatcher(Config{
		Staff:        staff,
		AdminBaseURL: "https://cozy.example.com/",
		Lookup: fakeLookup{info: OrderInfo{
			CustomerName: "Айбек", CustomerPhone: "+996700123456", AddressText: "Бишкек, Чуй 1 & кв. 5", PointName: "ЦУМ",
		}},
	})
	d.OrderCreated(deliveryOrder(orders.StatusPlaced))
	shutdown(t, d)

	if len(staff.msgs) != 1 {
		t.Fatalf("got %d staff messages, want 1", len(staff.msgs))
	}
	msg := staff.msgs[0]
	for _, want := range []string{
		"<b>Новый заказ COZY-20260923-007</b>",
		"Сумма: <b>12 500 сом</b>",
		"Оплата: наличными при получении",
		"доставка — Бишкек, Чуй 1 &amp; кв. 5",
		"Айбек +996700123456",
		"Air &lt;Max&gt; (42, Черный) × 2 — 10 000 сом",
		"Носки × 1 — 2 500 сом",
		`<a href="https://cozy.example.com/admin/orders/11111111-2222-3333-4444-555555555555">`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("staff message missing %q:\n%s", want, msg)
		}
	}
}

func TestOrderCreatedPickupAndLookupFailureStillSends(t *testing.T) {
	staff := &fakeStaff{}
	d := NewDispatcher(Config{Staff: staff, Lookup: fakeLookup{err: errors.New("db down")}})
	o := deliveryOrder(orders.StatusPlaced)
	o.AddressID = nil
	d.OrderCreated(o)
	shutdown(t, d)

	if len(staff.msgs) != 1 {
		t.Fatalf("got %d staff messages, want 1 despite lookup failure", len(staff.msgs))
	}
	if !strings.Contains(staff.msgs[0], "самовывоз") || strings.Contains(staff.msgs[0], "<a href") {
		t.Errorf("want pickup wording and no admin link without base URL:\n%s", staff.msgs[0])
	}
}

func TestZeroConfigDispatcherIsNoop(t *testing.T) {
	d := NewDispatcher(Config{})
	d.OrderCreated(deliveryOrder(orders.StatusPlaced))
	d.OrderStatusChanged(deliveryOrder(orders.StatusConfirmed), orders.StatusPlaced)
	shutdown(t, d)
}

func TestDispatcherIsBoundedAndNonBlocking(t *testing.T) {
	p := &fakePush{block: make(chan struct{}), started: make(chan struct{}, 10)}
	d := NewDispatcher(Config{
		Push:        p,
		Tokens:      &fakeTokens{tokens: map[string][]string{"cust-1": {"t"}}},
		MaxInFlight: 1,
	})

	begin := time.Now()
	d.OrderStatusChanged(deliveryOrder(orders.StatusConfirmed), orders.StatusPlaced)
	<-p.started                                                                         // first job is now in flight and blocked
	d.OrderStatusChanged(deliveryOrder(orders.StatusCancelled), orders.StatusConfirmed) // must be dropped, not queued
	if time.Since(begin) > time.Second {
		t.Fatal("OrderStatusChanged blocked the caller")
	}
	close(p.block)
	shutdown(t, d)

	if len(p.sent) != 1 {
		t.Errorf("sent %d, want 1 (second event dropped by the in-flight bound)", len(p.sent))
	}
	// After shutdown, new events are dropped rather than started.
	d.OrderCreated(deliveryOrder(orders.StatusPlaced))
}

func TestJobTimeoutCancelsSlowSend(t *testing.T) {
	p := &fakePush{block: make(chan struct{})} // never unblocked
	d := NewDispatcher(Config{
		Push:       p,
		Tokens:     &fakeTokens{tokens: map[string][]string{"cust-1": {"t"}}},
		JobTimeout: 50 * time.Millisecond,
	})
	d.OrderStatusChanged(deliveryOrder(orders.StatusConfirmed), orders.StatusPlaced)
	shutdown(t, d) // would hang (and fail at 5s) without the job timeout
}

func TestFormatSom(t *testing.T) {
	cases := map[float64]string{0: "0 сом", 999: "999 сом", 1000: "1 000 сом", 1234567.5: "1 234 567,50 сом"}
	for in, want := range cases {
		if got := formatSom(in); got != want {
			t.Errorf("formatSom(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestSQLOrderInfoLookup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	o := deliveryOrder(orders.StatusPlaced)
	mock.ExpectQuery(regexp.QuoteMeta("FROM customers c")).
		WithArgs("cust-1", o.AddressID, o.PointID).
		WillReturnRows(sqlmock.NewRows([]string{"name", "phone", "address_text", "p_name", "p_address"}).
			AddRow("Айбек", "+996700123456", "Чуй 1", "ЦУМ", "Чуй 155"))

	info, err := NewSQLOrderInfoLookup(db).OrderInfo(context.Background(), o)
	if err != nil {
		t.Fatalf("OrderInfo: %v", err)
	}
	want := OrderInfo{CustomerName: "Айбек", CustomerPhone: "+996700123456", AddressText: "Чуй 1", PointName: "ЦУМ", PointAddress: "Чуй 155"}
	if info != want {
		t.Errorf("info = %+v, want %+v", info, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestStaffMessageShowsDeliveryFeeAndPaidOnline(t *testing.T) {
	staff := &fakeStaff{}
	d := NewDispatcher(Config{Staff: staff})
	o := deliveryOrder(orders.StatusPlaced)
	o.DeliveryFee = 200
	o.TotalAmount = 12700
	paid := orders.PaymentPaid
	o.PaymentMethod, o.PaymentStatus = orders.PaymentOnlineCard, &paid
	d.OrderCreated(o)
	shutdown(t, d)

	if len(staff.msgs) != 1 {
		t.Fatalf("got %d staff messages", len(staff.msgs))
	}
	for _, want := range []string{"Сумма: <b>12 700 сом</b> (в т.ч. доставка 200 сом)", "Оплата: онлайн картой — оплачено"} {
		if !strings.Contains(staff.msgs[0], want) {
			t.Errorf("staff message missing %q:\n%s", want, staff.msgs[0])
		}
	}
}

func TestOrderCancelledByCustomerNotifiesStaff(t *testing.T) {
	staff := &fakeStaff{}
	d := NewDispatcher(Config{Staff: staff, AdminBaseURL: "https://cozy.example.com"})
	d.OrderCancelledByCustomer(deliveryOrder(orders.StatusCancelled))
	shutdown(t, d)

	if len(staff.msgs) != 1 || !strings.Contains(staff.msgs[0], "Покупатель отменил заказ COZY-20260923-007") ||
		!strings.Contains(staff.msgs[0], "/admin/orders/11111111-2222-3333-4444-555555555555") {
		t.Fatalf("staff messages = %q", staff.msgs)
	}
}
