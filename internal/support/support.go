// Package support is customer support requests ("обращения"): a customer
// (or a guest on the site) writes to the shop from the site or the app,
// staff see the requests in the admin panel (Обращения), answer them and
// move them through new → in_progress → closed; the customer sees the
// status and the answers.
//
// Business rules live here only (web, the JSON API and the admin panel
// call Service): validation, which order may be attached, rate limits,
// who may write when, and the notifications fired after a commit.
package support

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/phonenorm"
)

// Topic is the fixed list of request subjects.
type Topic string

const (
	TopicOrder    Topic = "order"
	TopicPayment  Topic = "payment"
	TopicDelivery Topic = "delivery"
	TopicReturn   Topic = "return" // возврат / обмен
	TopicProduct  Topic = "product"
	TopicOther    Topic = "other"
)

// Topics lists every topic in display order.
var Topics = []Topic{TopicOrder, TopicPayment, TopicDelivery, TopicReturn, TopicProduct, TopicOther}

// Valid reports whether t is one of Topics.
func (t Topic) Valid() bool {
	for _, v := range Topics {
		if v == t {
			return true
		}
	}
	return false
}

// Status is where a request is in its lifecycle.
type Status string

const (
	StatusNew        Status = "new"
	StatusInProgress Status = "in_progress"
	StatusClosed     Status = "closed"
)

// Statuses lists every status in lifecycle order.
var Statuses = []Status{StatusNew, StatusInProgress, StatusClosed}

// Valid reports whether s is one of Statuses.
func (s Status) Valid() bool {
	return s == StatusNew || s == StatusInProgress || s == StatusClosed
}

// Channel is where the request was written.
type Channel string

const (
	ChannelWeb Channel = "web"
	ChannelApp Channel = "app"
)

// Author says who wrote a message.
type Author string

const (
	AuthorCustomer Author = "customer"
	AuthorStaff    Author = "staff"
)

// Limits on what a customer may send.
const (
	MaxMessageLen = 2000 // characters (runes) per message
	MaxNameLen    = 100
	// MaxTicketsPerHour bounds new requests per customer (and per phone,
	// which is how guests are counted) in a rolling hour.
	MaxTicketsPerHour = 5
	// MaxCustomerMessagesPerHour bounds a customer's follow-ups across
	// all their requests in a rolling hour.
	MaxCustomerMessagesPerHour = 30
)

// Message is one entry of a request's conversation.
type Message struct {
	ID        string
	Author    Author
	StaffID   *string
	StaffName string // admin view only; "" when unknown or deleted
	Body      string
	CreatedAt time.Time
}

// Ticket is one support request with its conversation (oldest first).
type Ticket struct {
	ID          string
	Number      int64
	CustomerID  *string
	Name        string
	Phone       string
	Topic       Topic
	OrderID     *string
	OrderNumber string // "" when no order is attached
	Channel     Channel
	Status      Status
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ClosedAt    *time.Time
	Messages    []Message
}

// IsGuest reports whether the request came from a visitor without an account.
func (t Ticket) IsGuest() bool { return t.CustomerID == nil }

// FirstMessage is the request text (the first customer message).
func (t Ticket) FirstMessage() string {
	if len(t.Messages) == 0 {
		return ""
	}
	return t.Messages[0].Body
}

// Summary is one row of a request list: the request plus its latest
// message (no full conversation).
type Summary struct {
	ID            string
	Number        int64
	CustomerID    *string
	Name          string
	Phone         string
	Topic         Topic
	OrderID       *string
	OrderNumber   string
	Channel       Channel
	Status        Status
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ClosedAt      *time.Time
	MessagesCount int
	LastMessage   Message
}

// CreateInput is a new request. CustomerID "" means a guest (site only):
// then Name and Phone are required. For a logged-in customer blank
// Name/Phone default to the account's.
type CreateInput struct {
	CustomerID string
	Name       string
	Phone      string
	Topic      Topic
	Message    string
	OrderID    string
	Channel    Channel
}

// ListFilter narrows the admin list. Zero values mean "any".
type ListFilter struct {
	Status   Status
	Topic    Topic
	Query    string // phone digits or part of the name
	Page     int    // 1-based
	PageSize int
}

// Errors. Russian messages are the source text (locales/errors.*.yaml).
var (
	errTicketNotFound   = apperr.NotFound("ticket_not_found", "обращение не найдено")
	errTicketClosed     = apperr.Conflict("ticket_closed", "обращение закрыто — создайте новое, если вопрос остался")
	errInvalidTopic     = apperr.BadRequest("invalid_ticket_topic", "выберите тему обращения")
	errInvalidStatus    = apperr.BadRequest("invalid_ticket_status", "неизвестный статус обращения")
	errInvalidChannel   = apperr.BadRequest("invalid_ticket_channel", "неизвестный канал обращения")
	errMessageEmpty     = apperr.BadRequest("message_required", "напишите текст сообщения")
	errMessageLong      = apperr.BadRequest("message_too_long", "сообщение длиннее 2000 символов")
	errNameRequired     = apperr.BadRequest("name_required", "укажите имя")
	errNameLong         = apperr.BadRequest("name_too_long", "имя длиннее 100 символов")
	errInvalidPhone     = apperr.BadRequest("invalid_phone", "укажите номер Кыргызстана, например 0700 123 456 или +996 700 123 456")
	errInvalidOrder     = apperr.BadRequest("invalid_ticket_order", "этот заказ не найден среди ваших заказов")
	errTooManyTickets   = apperr.TooManyRequests("too_many_tickets", "слишком много обращений за час — подождите немного или позвоните нам")
	errCustomerNotFound = apperr.NotFound("customer_not_found", "покупатель не найден")
	errTooManyMsgs      = apperr.TooManyRequests("too_many_ticket_messages", "слишком много сообщений подряд — подождите немного")
)

// ErrTicketNotFound is the 404 for a request that doesn't exist or isn't
// the caller's (indistinguishable on purpose).
func ErrTicketNotFound() error { return errTicketNotFound }

// normalizeMessage trims body and checks its length.
func normalizeMessage(body string) (string, error) {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if body == "" {
		return "", errMessageEmpty
	}
	if utf8.RuneCountInString(body) > MaxMessageLen {
		return "", errMessageLong
	}
	return body, nil
}

// normalizeName collapses whitespace and checks the length; required
// applies to guests.
func normalizeName(name string, required bool) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" && required {
		return "", errNameRequired
	}
	if utf8.RuneCountInString(name) > MaxNameLen {
		return "", errNameLong
	}
	return name, nil
}

// normalizePhone returns the canonical +996XXXXXXXXX form.
func normalizePhone(raw string) (string, error) {
	p, ok := phonenorm.E164(raw)
	if !ok {
		return "", errInvalidPhone
	}
	return p, nil
}
