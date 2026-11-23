package notifications

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/phonenorm"
	"github.com/Nikemas/cozy_backend/internal/push"
	"github.com/Nikemas/cozy_backend/internal/support"
)

// PushTypeSupportReply is data["type"] on the push sent when staff answer
// a support request; data["ticket_id"] is the request id.
const PushTypeSupportReply = "support_reply"

var _ support.Notifier = (*Dispatcher)(nil)

// Text limits: a Telegram message must stay under 4096 characters, a push
// body is shown truncated by the OS anyway.
const (
	maxStaffTicketText = 1500
	maxPushReplyText   = 150
)

// supportTopicLabels are the staff-facing (Russian) topic names.
var supportTopicLabels = map[support.Topic]string{
	support.TopicOrder:    "Заказ",
	support.TopicPayment:  "Оплата",
	support.TopicDelivery: "Доставка",
	support.TopicReturn:   "Возврат / обмен",
	support.TopicProduct:  "Товар",
	support.TopicOther:    "Другое",
}

// supportReplyTitles[lang] is the reply push title; %d is the number.
var supportReplyTitles = map[string]string{
	i18n.LangRU: "Ответ на обращение №%d",
	i18n.LangKY: "№%d кайрылууңузга жооп келди",
}

func ticketRef(t support.Ticket) string { return "ticket#" + strconv.FormatInt(t.Number, 10) }

// TicketCreated sends staff a Telegram message about a new request.
func (d *Dispatcher) TicketCreated(t support.Ticket) {
	d.goJob("ticket_created", ticketRef(t), func(ctx context.Context) {
		text := staffTicketMessage("Новое обращение", t, t.FirstMessage(), d.cfg.AdminBaseURL)
		if err := d.cfg.Staff.SendStaffMessage(ctx, text); err != nil {
			slog.Error("notifications: staff new-ticket message failed", "ticket_number", t.Number, "err", err)
		}
	})
}

// CustomerMessageAdded tells staff the customer followed up.
func (d *Dispatcher) CustomerMessageAdded(t support.Ticket, m support.Message) {
	d.goJob("ticket_customer_message", ticketRef(t), func(ctx context.Context) {
		text := staffTicketMessage("Новое сообщение в обращении", t, m.Body, d.cfg.AdminBaseURL)
		if err := d.cfg.Staff.SendStaffMessage(ctx, text); err != nil {
			slog.Error("notifications: staff ticket follow-up message failed", "ticket_number", t.Number, "err", err)
		}
	})
}

// StaffReplied pushes the answer to every device of the request's
// customer. Guests (no account) and customers without the app get
// nothing here — they see the answer on the site / get a call.
func (d *Dispatcher) StaffReplied(t support.Ticket, m support.Message) {
	if t.CustomerID == nil {
		return
	}
	d.goJob("ticket_staff_reply", ticketRef(t), func(ctx context.Context) {
		d.pushTicketReply(ctx, t, m)
	})
}

func (d *Dispatcher) pushTicketReply(ctx context.Context, t support.Ticket, m support.Message) {
	if d.cfg.Tokens == nil {
		return
	}
	customerID := *t.CustomerID
	tokens, err := d.cfg.Tokens.TokensForCustomer(ctx, customerID)
	if err != nil {
		slog.Error("notifications: loading device tokens failed", "ticket_number", t.Number, "err", err)
		return
	}
	if len(tokens) == 0 {
		return
	}
	lang := d.contactLang(ctx, customerID)
	msg := push.Message{
		Title: fmt.Sprintf(supportReplyTitles[lang], t.Number),
		Body:  truncateRunes(m.Body, maxPushReplyText),
		Data:  map[string]string{"type": PushTypeSupportReply, "ticket_id": t.ID},
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
				slog.Error("notifications: deleting invalid device token failed", "ticket_number", t.Number, "err", delErr)
			}
		default:
			failed++
			slog.Error("notifications: ticket reply push failed", "ticket_number", t.Number, "err", err)
		}
	}
	slog.Info("notifications: ticket reply push", "ticket_number", t.Number, "lang", lang,
		"sent", sent, "invalid_tokens_removed", dropped, "failed", failed)
}

// contactLang is the customer's language, default on lookup failure.
func (d *Dispatcher) contactLang(ctx context.Context, customerID string) string {
	if d.cfg.Contacts == nil {
		return i18n.DefaultLang
	}
	c, err := d.cfg.Contacts.CustomerContact(ctx, customerID)
	if err != nil {
		slog.Warn("notifications: customer contact lookup failed", "err", err)
		return i18n.DefaultLang
	}
	if c.Lang != i18n.LangRU && c.Lang != i18n.LangKY {
		return i18n.DefaultLang
	}
	return c.Lang
}

// staffTicketMessage renders the Telegram-HTML staff note about request
// t with body as the quoted text. Every value is HTML-escaped.
func staffTicketMessage(heading string, t support.Ticket, body, adminBaseURL string) string {
	e := html.EscapeString
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s №%d</b>\n", e(heading), t.Number)
	fmt.Fprintf(&b, "Тема: %s\n", e(supportTopicLabels[t.Topic]))
	channel := "сайт"
	if t.Channel == support.ChannelApp {
		channel = "приложение"
	}
	if t.IsGuest() {
		channel += ", без входа"
	}
	fmt.Fprintf(&b, "Канал: %s\n", channel)
	who := strings.TrimSpace(t.Name + " " + phonenorm.Display(t.Phone))
	fmt.Fprintf(&b, "Клиент: %s\n", e(who))
	if t.OrderNumber != "" {
		fmt.Fprintf(&b, "Заказ: %s\n", e(t.OrderNumber))
	}
	fmt.Fprintf(&b, "\n%s\n", e(truncateRunes(body, maxStaffTicketText)))
	if base := strings.TrimRight(adminBaseURL, "/"); base != "" {
		fmt.Fprintf(&b, "\n<a href=\"%s/admin/tickets/%s\">Открыть в админке</a>", e(base), e(t.ID))
	}
	return b.String()
}

// truncateRunes cuts s to at most n runes, ending with "…" when cut.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
