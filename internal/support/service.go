package support

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// Page sizes of request lists.
const (
	DefaultPageSize = 20
	MaxPageSize     = 50
)

// Service is the support-request business logic shared by the JSON API,
// the site and the admin panel.
type Service struct {
	store    Store
	notifier Notifier
	now      func() time.Time
}

// NewService returns a Service over Postgres.
func NewService(db *sql.DB) *Service { return NewServiceWithStore(NewSQLStore(db)) }

// NewServiceWithStore returns a Service over store (unit tests).
func NewServiceWithStore(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

// WithNotifier overrides the process default Notifier (tests). Returns s.
func (s *Service) WithNotifier(n Notifier) *Service {
	s.notifier = n
	return s
}

// validID reports whether id can be a request id; anything else is
// treated as "not found" without touching the database.
func validID(id string) bool { return uuid.Validate(id) == nil }

// Create validates and stores a new request, then tells staff.
func (s *Service) Create(ctx context.Context, in CreateInput) (*Ticket, error) {
	nt, err := s.validateCreate(ctx, in)
	if err != nil {
		return nil, err
	}
	id, err := s.store.Create(ctx, nt, MaxTicketsPerHour)
	if err != nil {
		return nil, err
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.notify("ticket_created", t.Number, func(n Notifier) { n.TicketCreated(*t) })
	return t, nil
}

func (s *Service) validateCreate(ctx context.Context, in CreateInput) (NewTicket, error) {
	if in.Channel != ChannelWeb && in.Channel != ChannelApp {
		return NewTicket{}, errInvalidChannel
	}
	if !in.Topic.Valid() {
		return NewTicket{}, errInvalidTopic
	}
	body, err := normalizeMessage(in.Message)
	if err != nil {
		return NewTicket{}, err
	}
	nt := NewTicket{Topic: in.Topic, Channel: in.Channel, Message: body}
	if in.CustomerID == "" {
		return s.guestContact(nt, in)
	}
	return s.customerContact(ctx, nt, in)
}

// guestContact: name and phone are required, no order can be attached
// (a guest can't prove the order is theirs).
func (s *Service) guestContact(nt NewTicket, in CreateInput) (NewTicket, error) {
	name, err := normalizeName(in.Name, true)
	if err != nil {
		return NewTicket{}, err
	}
	phone, err := normalizePhone(in.Phone)
	if err != nil {
		return NewTicket{}, err
	}
	if in.OrderID != "" {
		return NewTicket{}, errInvalidOrder
	}
	nt.Name, nt.Phone = name, phone
	return nt, nil
}

// customerContact defaults a blank name/phone to the account's and checks
// the attached order belongs to the customer.
func (s *Service) customerContact(ctx context.Context, nt NewTicket, in CreateInput) (NewTicket, error) {
	accName, accPhone, found, err := s.store.CustomerContact(ctx, in.CustomerID)
	if err != nil {
		return NewTicket{}, err
	}
	if !found {
		return NewTicket{}, errCustomerNotFound
	}
	name, err := normalizeName(in.Name, false)
	if err != nil {
		return NewTicket{}, err
	}
	if name == "" {
		name, _ = normalizeName(accName, false)
	}
	phone := accPhone
	if in.Phone != "" {
		if phone, err = normalizePhone(in.Phone); err != nil {
			return NewTicket{}, err
		}
	}
	customerID := in.CustomerID
	nt.CustomerID, nt.Name, nt.Phone = &customerID, name, phone
	if in.OrderID != "" {
		if !validID(in.OrderID) {
			return NewTicket{}, errInvalidOrder
		}
		ok, err := s.store.OrderOfCustomer(ctx, in.OrderID, in.CustomerID)
		if err != nil {
			return NewTicket{}, err
		}
		if !ok {
			return NewTicket{}, errInvalidOrder
		}
		orderID := in.OrderID
		nt.OrderID = &orderID
	}
	return nt, nil
}

// NormalizePage clamps page/pageSize to valid values (page ≥ 1,
// 1 ≤ pageSize ≤ MaxPageSize, 0 → DefaultPageSize).
func NormalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}
	return page, pageSize
}

// ListForCustomer is the customer's own requests, most recently active
// first, and their total count.
func (s *Service) ListForCustomer(ctx context.Context, customerID string, page, pageSize int) ([]Summary, int, error) {
	page, pageSize = NormalizePage(page, pageSize)
	return s.store.ListByCustomer(ctx, customerID, pageSize, (page-1)*pageSize)
}

// GetForCustomer returns one of the customer's requests; someone else's
// (or a guest's) is errTicketNotFound, same as a missing one.
func (s *Service) GetForCustomer(ctx context.Context, customerID, id string) (*Ticket, error) {
	if customerID == "" || !validID(id) {
		return nil, errTicketNotFound
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.CustomerID == nil || *t.CustomerID != customerID {
		return nil, errTicketNotFound
	}
	return t, nil
}

// AddCustomerMessage appends the customer's follow-up to their own open
// request (errTicketClosed once closed) and tells staff.
func (s *Service) AddCustomerMessage(ctx context.Context, customerID, id, body string) (*Ticket, error) {
	text, err := normalizeMessage(body)
	if err != nil {
		return nil, err
	}
	if _, err := s.GetForCustomer(ctx, customerID, id); err != nil {
		return nil, err
	}
	n, err := s.store.CustomerMessagesSince(ctx, customerID, s.now().Add(-time.Hour))
	if err != nil {
		return nil, err
	}
	if n >= MaxCustomerMessagesPerHour {
		return nil, errTooManyMsgs
	}
	m, err := s.store.AddMessage(ctx, id, AuthorCustomer, nil, text)
	if err != nil {
		return nil, err
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.notify("ticket_customer_message", t.Number, func(n Notifier) { n.CustomerMessageAdded(*t, m) })
	return t, nil
}

// --- staff (admin panel) ---

// List is the admin list with filters; invalid filter values are errors.
func (s *Service) List(ctx context.Context, f ListFilter) ([]Summary, int, error) {
	if f.Status != "" && !f.Status.Valid() {
		return nil, 0, errInvalidStatus
	}
	if f.Topic != "" && !f.Topic.Valid() {
		return nil, 0, errInvalidTopic
	}
	f.Page, f.PageSize = NormalizePage(f.Page, f.PageSize)
	return s.store.List(ctx, f)
}

// Get returns any request (staff).
func (s *Service) Get(ctx context.Context, id string) (*Ticket, error) {
	if !validID(id) {
		return nil, errTicketNotFound
	}
	return s.store.Get(ctx, id)
}

// Reply adds a staff answer (a new request becomes in_progress) and
// pushes it to the customer.
func (s *Service) Reply(ctx context.Context, id, staffID, body string) (*Ticket, error) {
	if !validID(id) {
		return nil, errTicketNotFound
	}
	text, err := normalizeMessage(body)
	if err != nil {
		return nil, err
	}
	var sid *string
	if staffID != "" {
		sid = &staffID
	}
	m, err := s.store.AddMessage(ctx, id, AuthorStaff, sid, text)
	if err != nil {
		return nil, err
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.notify("ticket_staff_reply", t.Number, func(n Notifier) { n.StaffReplied(*t, m) })
	return t, nil
}

// SetStatus moves a request to status (any transition is allowed, so
// staff can reopen a closed one) and returns it with the previous status.
func (s *Service) SetStatus(ctx context.Context, id string, status Status) (*Ticket, Status, error) {
	if !validID(id) {
		return nil, "", errTicketNotFound
	}
	if !status.Valid() {
		return nil, "", errInvalidStatus
	}
	from, err := s.store.SetStatus(ctx, id, status)
	if err != nil {
		return nil, "", err
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	return t, from, nil
}

// CountNew is the number of unanswered new requests (sidebar badge).
func (s *Service) CountNew(ctx context.Context) (int, error) {
	return s.store.CountNew(ctx)
}
