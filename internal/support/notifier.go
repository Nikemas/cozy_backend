package support

import (
	"log/slog"
	"sync/atomic"
)

// Notifier is told about request events after they have committed — the
// only hook this package has into Telegram/push delivery (implemented by
// internal/notifications). Implementations must return quickly and do the
// delivery asynchronously; a notification failure never fails a request.
type Notifier interface {
	// TicketCreated: a new request (staff Telegram).
	TicketCreated(t Ticket)
	// CustomerMessageAdded: the customer followed up (staff Telegram).
	CustomerMessageAdded(t Ticket, m Message)
	// StaffReplied: staff answered (customer push, if they have a device).
	StaffReplied(t Ticket, m Message)
}

type notifierHolder struct{ n Notifier }

var defaultNotifier atomic.Value // notifierHolder

// SetDefaultNotifier installs the process-wide Notifier for every Service
// without one of its own (called once from cmd/server, like
// orders.SetDefaultNotifier). nil resets to no-op.
func SetDefaultNotifier(n Notifier) {
	defaultNotifier.Store(notifierHolder{n: n})
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

// notify calls fn with the current notifier, shielding the caller from a
// panicking implementation.
func (s *Service) notify(event string, number int64, fn func(Notifier)) {
	n := s.currentNotifier()
	if n == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("support: notifier panicked", "event", event, "ticket_number", number, "panic", r)
		}
	}()
	fn(n)
}
