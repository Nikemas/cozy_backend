package orders

import (
	"log/slog"
	"sync/atomic"
)

// Notifier is told about order lifecycle events AFTER the transaction that
// caused them has committed. It is the only hook the orders package has
// into push/Telegram delivery (see internal/notifications), so this
// package never depends on a transport.
//
// Contract for implementations: both methods must return quickly and must
// not block on network I/O — do the actual delivery asynchronously. The
// orders package calls them on the request goroutine and ignores any
// outcome: a notification failure never fails an order operation.
type Notifier interface {
	// OrderCreated fires once a new order (with Items) has committed.
	OrderCreated(order Order)
	// OrderStatusChanged fires once an admin status change has committed;
	// from is the status the order had before the change.
	OrderStatusChanged(order Order, from OrderStatus)
}

// notifierHolder wraps a Notifier so atomic.Value always stores one
// concrete type.
type notifierHolder struct{ n Notifier }

var defaultNotifier atomic.Value // notifierHolder

// SetDefaultNotifier installs the process-wide Notifier used by every
// Service that has none of its own. Called once from cmd/server/main.go
// before routes are registered — Services are constructed in several
// packages (web, admin, httpapi), so a process default avoids threading a
// notifier through each of their Register* signatures. nil resets to
// no-op.
func SetDefaultNotifier(n Notifier) {
	defaultNotifier.Store(notifierHolder{n: n})
}

// WithNotifier sets a Service-specific Notifier (used by tests), overriding
// the process default. Returns s for chaining.
func (s *Service) WithNotifier(n Notifier) *Service {
	s.notifier = n
	return s
}

func (s *Service) currentNotifier() Notifier {
	if s.notifier != nil {
		return s.notifier
	}
	if h, ok := defaultNotifier.Load().(notifierHolder); ok {
		return h.n
	}
	return nil
}

// notifyCreated/notifyStatusChanged shield callers from a misbehaving
// Notifier: a panic is logged and swallowed so it can never turn a
// committed order into a 500.
func (s *Service) notifyCreated(o Order) {
	n := s.currentNotifier()
	if n == nil {
		return
	}
	defer recoverNotifier("order_created", o.OrderNumber)
	n.OrderCreated(o)
}

func (s *Service) notifyStatusChanged(o Order, from OrderStatus) {
	n := s.currentNotifier()
	if n == nil {
		return
	}
	defer recoverNotifier("order_status_changed", o.OrderNumber)
	n.OrderStatusChanged(o, from)
}

func recoverNotifier(event, orderNumber string) {
	if r := recover(); r != nil {
		slog.Error("orders: notifier panicked", "event", event, "order_number", orderNumber, "panic", r)
	}
}
