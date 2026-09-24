// Package notifications implements orders.Notifier: it turns committed
// order events into a customer push (FCM, internal/push) on status change
// and a staff Telegram message (internal/notify) on new orders.
//
// Everything runs on background goroutines — bounded by a semaphore and a
// per-job timeout — so a slow or down FCM/Telegram never slows down or
// fails an order request. Failures are only logged.
package notifications

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/notify"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/push"
)

const (
	// AndroidChannelOrders is the notification channel id the Flutter app
	// registers for order updates (agreed contract with the mobile side).
	AndroidChannelOrders = "orders"
	// PushTypeOrderStatus is data["type"] on order-status pushes.
	PushTypeOrderStatus = "order_status"

	defaultMaxInFlight = 16
	defaultJobTimeout  = 20 * time.Second
)

// TokenStore is the device-token subset of storefront.DeviceTokenRepo.
type TokenStore interface {
	TokensForCustomer(ctx context.Context, customerID string) ([]string, error)
	DeleteToken(ctx context.Context, fcmToken string) error
}

// OrderInfo is display context for the staff message that isn't on
// orders.Order itself. Any field may be empty if lookup failed.
type OrderInfo struct {
	CustomerName  string
	CustomerPhone string
	AddressText   string
	PointName     string
	PointAddress  string
}

// OrderInfoLookup resolves OrderInfo for an order (see SQLOrderInfoLookup).
type OrderInfoLookup interface {
	OrderInfo(ctx context.Context, o orders.Order) (OrderInfo, error)
}

// CustomerContact is what a customer-facing notification needs to know
// about its recipient. Phone is empty for a deleted (anonymized) customer.
type CustomerContact struct {
	Lang  string // i18n.LangRU / i18n.LangKY; empty → default language
	Phone string
}

// ContactLookup resolves a customer's language and phone (see
// SQLContactLookup).
type ContactLookup interface {
	CustomerContact(ctx context.Context, customerID string) (CustomerContact, error)
}

// Config wires a Dispatcher. Push/Staff default to the no-op senders,
// so a zero Config is a valid "everything disabled" dispatcher.
type Config struct {
	Push   push.Sender
	Tokens TokenStore
	Staff  notify.StaffMessenger
	Lookup OrderInfoLookup
	// Contacts resolves the customer's language (customers.lang) and phone
	// for order-status notifications; nil → Russian, no SMS fallback.
	Contacts ContactLookup
	// SMS + SMSFallback: when SMSFallback is on and an order-status
	// update reaches none of the customer's devices (no device tokens, or
	// all of them invalid), the title is texted to the customer's phone
	// instead. Off by default (env SMS_STATUS_FALLBACK) — every SMS costs
	// money.
	SMS         notify.SMSSender
	SMSFallback bool
	// AdminBaseURL (e.g. "https://cozy.kg") prefixes the admin order link
	// in staff messages; empty omits the link.
	AdminBaseURL string
	// MaxInFlight bounds concurrent background jobs; excess events are
	// dropped (and logged) rather than queued without limit.
	MaxInFlight int
	// JobTimeout bounds one job (all pushes for one event, or one
	// Telegram message).
	JobTimeout time.Duration
}

// Dispatcher implements orders.Notifier.
type Dispatcher struct {
	cfg Config
	sem chan struct{}
	wg  sync.WaitGroup

	mu     sync.Mutex
	closed bool
}

var (
	_ orders.Notifier       = (*Dispatcher)(nil)
	_ orders.CancelNotifier = (*Dispatcher)(nil)
)

func NewDispatcher(cfg Config) *Dispatcher {
	if cfg.Push == nil {
		cfg.Push = push.NopSender{}
	}
	if cfg.Staff == nil {
		cfg.Staff = notify.NopStaffMessenger{}
	}
	if cfg.SMS == nil {
		cfg.SMS = notify.NopSMSSender{}
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = defaultMaxInFlight
	}
	if cfg.JobTimeout <= 0 {
		cfg.JobTimeout = defaultJobTimeout
	}
	return &Dispatcher{cfg: cfg, sem: make(chan struct{}, cfg.MaxInFlight)}
}

// OrderCreated sends the staff Telegram message in the background.
func (d *Dispatcher) OrderCreated(o orders.Order) {
	d.goJob("order_created", o.OrderNumber, func(ctx context.Context) {
		d.sendStaffNewOrder(ctx, o)
	})
}

// OrderCancelledByCustomer tells staff (Telegram) that a customer
// cancelled their order, so nobody keeps packing it.
func (d *Dispatcher) OrderCancelledByCustomer(o orders.Order) {
	d.goJob("order_cancelled_by_customer", o.OrderNumber, func(ctx context.Context) {
		text := staffCancelledMessage(o, d.cfg.AdminBaseURL)
		if err := d.cfg.Staff.SendStaffMessage(ctx, text); err != nil {
			slog.Error("notifications: staff cancel message failed", "order_number", o.OrderNumber, "err", err)
		}
	})
}

// OrderStatusChanged pushes the new status to every device of the
// order's customer in the background.
func (d *Dispatcher) OrderStatusChanged(o orders.Order, from orders.OrderStatus) {
	d.goJob("order_status_changed", o.OrderNumber, func(ctx context.Context) {
		d.pushStatus(ctx, o, from)
	})
}

// Shutdown stops accepting new jobs and waits (up to ctx) for in-flight
// ones, so a deploy restart doesn't cut a just-created order's
// notification in half.
func (d *Dispatcher) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()

	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Dispatcher) goJob(event, orderNumber string, job func(ctx context.Context)) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		slog.Warn("notifications: dispatcher shut down, event dropped", "event", event, "order_number", orderNumber)
		return
	}
	select {
	case d.sem <- struct{}{}:
	default:
		d.mu.Unlock()
		slog.Warn("notifications: too many in-flight notifications, event dropped",
			"event", event, "order_number", orderNumber, "max_in_flight", d.cfg.MaxInFlight)
		return
	}
	d.wg.Add(1)
	d.mu.Unlock()

	go func() {
		defer d.wg.Done()
		defer func() { <-d.sem }()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("notifications: job panicked", "event", event, "order_number", orderNumber, "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), d.cfg.JobTimeout)
		defer cancel()
		job(ctx)
	}()
}

func (d *Dispatcher) sendStaffNewOrder(ctx context.Context, o orders.Order) {
	var info OrderInfo
	if d.cfg.Lookup != nil {
		var err error
		info, err = d.cfg.Lookup.OrderInfo(ctx, o)
		if err != nil {
			// Still send the message — number, total and items are enough
			// for staff to act on.
			slog.Warn("notifications: order info lookup failed", "order_number", o.OrderNumber, "err", err)
		}
	}
	text := staffOrderMessage(o, info, d.cfg.AdminBaseURL)
	if err := d.cfg.Staff.SendStaffMessage(ctx, text); err != nil {
		slog.Error("notifications: staff new-order message failed", "order_number", o.OrderNumber, "err", err)
		return
	}
	slog.Info("notifications: staff notified about new order", "order_number", o.OrderNumber)
}

func (d *Dispatcher) pushStatus(ctx context.Context, o orders.Order, from orders.OrderStatus) {
	if _, _, ok := statusPushText(i18n.DefaultLang, o); !ok {
		return // this status has no customer-facing notification
	}
	contact := d.customerContact(ctx, o)
	title, body, _ := statusPushText(contact.Lang, o)

	var tokens []string
	if d.cfg.Tokens != nil {
		var err error
		tokens, err = d.cfg.Tokens.TokensForCustomer(ctx, o.CustomerID)
		if err != nil {
			// Unknown whether a push would have reached them — don't
			// fall back to a paid SMS on a DB hiccup.
			slog.Error("notifications: loading device tokens failed", "order_number", o.OrderNumber, "err", err)
			return
		}
	}

	msg := push.Message{
		Title: title,
		Body:  body,
		Data: map[string]string{
			"type":     PushTypeOrderStatus,
			"order_id": o.ID,
			"status":   string(o.Status),
		},
		AndroidChannelID: AndroidChannelOrders,
	}
	var sent, dropped, failed int
	for _, tok := range tokens {
		err := d.cfg.Push.Send(ctx, tok, msg)
		switch {
		case err == nil:
			sent++
		case errors.Is(err, push.ErrInvalidToken):
			dropped++
			if delErr := d.cfg.Tokens.DeleteToken(ctx, tok); delErr != nil {
				slog.Error("notifications: deleting invalid device token failed", "order_number", o.OrderNumber, "err", delErr)
			}
		default:
			failed++
			slog.Error("notifications: push send failed", "order_number", o.OrderNumber, "err", err)
		}
	}
	if len(tokens) > 0 {
		slog.Info("notifications: order status push",
			"order_number", o.OrderNumber, "from", from, "to", o.Status, "lang", contact.Lang,
			"sent", sent, "invalid_tokens_removed", dropped, "failed", failed)
	}

	// No device reached and none merely failing transiently → the customer
	// has no working app install: text them instead (if enabled).
	if sent == 0 && failed == 0 && d.cfg.SMSFallback {
		d.smsStatus(ctx, o, contact)
	}
}

// customerContact loads the recipient's language/phone; on failure it
// logs and falls back to the default language with no phone.
func (d *Dispatcher) customerContact(ctx context.Context, o orders.Order) CustomerContact {
	var c CustomerContact
	if d.cfg.Contacts != nil {
		var err error
		c, err = d.cfg.Contacts.CustomerContact(ctx, o.CustomerID)
		if err != nil {
			slog.Warn("notifications: customer contact lookup failed", "order_number", o.OrderNumber, "err", err)
			c = CustomerContact{}
		}
	}
	if c.Lang != i18n.LangRU && c.Lang != i18n.LangKY {
		c.Lang = i18n.DefaultLang
	}
	return c
}

func (d *Dispatcher) smsStatus(ctx context.Context, o orders.Order, c CustomerContact) {
	if c.Phone == "" {
		return // deleted customer or lookup failed
	}
	text, ok := statusSMSText(c.Lang, o)
	if !ok {
		return
	}
	if err := d.cfg.SMS.SendSMS(ctx, c.Phone, text); err != nil {
		slog.Error("notifications: order status SMS failed", "order_number", o.OrderNumber, "err", err)
		return
	}
	slog.Info("notifications: order status SMS fallback", "order_number", o.OrderNumber, "to", o.Status)
}
