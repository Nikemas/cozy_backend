// tickets_page.go (feat/support-tickets): Обращения — customer support
// requests from the site and the app. List with status chips, topic
// filter and search (name / phone / number); detail page with the
// customer's contacts, the attached order, the conversation, a reply form
// and status buttons. Owner and manager only. Status changes are written
// to the audit journal.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
	"github.com/Nikemas/cozy_backend/internal/support"
)

const (
	ticketsListPath  = "/admin/tickets"
	ticketsPageSize  = 30
	ticketPreviewLen = 140
	ticketDateLayout = "02.01.2006 15:04"
)

// ticketsBackend is what the Обращения screens use of *support.Service.
type ticketsBackend interface {
	List(ctx context.Context, f support.ListFilter) ([]support.Summary, int, error)
	Get(ctx context.Context, id string) (*support.Ticket, error)
	Reply(ctx context.Context, id, staffID, body string) (*support.Ticket, error)
	SetStatus(ctx context.Context, id string, status support.Status) (*support.Ticket, support.Status, error)
	CountNew(ctx context.Context) (int, error)
}

// registerTicketRoutes mounts the Обращения screens behind mw (owner +
// manager, see navDefs).
func registerTicketRoutes(mux *http.ServeMux, h *handlers, mw func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET "+ticketsListPath, mw(h.ticketsListPage))
	mux.HandleFunc("GET "+ticketsListPath+"/badge", mw(h.ticketsBadge))
	mux.HandleFunc("GET "+ticketsListPath+"/{id}", mw(h.ticketDetailPage))
	mux.HandleFunc("POST "+ticketsListPath+"/{id}/reply", mw(h.ticketReply))
	mux.HandleFunc("POST "+ticketsListPath+"/{id}/status", mw(h.ticketStatusUpdate))
}

// ticketStatusMeta is a status's label key and chip class.
func ticketStatusMeta(s support.Status) (labelKey, class string) {
	switch s {
	case support.StatusInProgress:
		return "admin.tickets.status.in_progress", "admin-chip--confirmed"
	case support.StatusClosed:
		return "admin.tickets.status.closed", "admin-chip--delivered"
	default:
		return "admin.tickets.status.new", "admin-chip--new"
	}
}

func ticketChannelKey(c support.Channel) string {
	if c == support.ChannelApp {
		return "admin.tickets.channel.app"
	}
	return "admin.tickets.channel.web"
}

func ticketTopicKey(t support.Topic) string { return "admin.tickets.topic." + string(t) }

func truncateRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// ---------- list ----------

// TicketRowVM is one list row.
type TicketRowVM struct {
	ID           string
	URL          string
	NumberLabel  string
	DateLabel    string
	Name         string
	Phone        string
	Guest        bool
	TopicLabel   string
	ChannelLabel string
	StatusLabel  string
	StatusClass  string
	Preview      string
	FromStaff    bool // the latest message is a staff reply
}

// TicketsListData backs tickets.gohtml.
type TicketsListData struct {
	Status       string
	Topic        string
	Query        string
	StatusChips  []StatusChipLink
	TopicOptions []AuditOptionVM
	Rows         []TicketRowVM
	Empty        bool
	Filtered     bool
	CountLabel   string
	HasPrev      bool
	HasNext      bool
	PrevURL      string
	NextURL      string
	ResetURL     string
	Err          string
}

type ticketsParams struct {
	Status, Topic, Q string
	Page             int
}

func parseTicketsParams(q url.Values) ticketsParams {
	p := ticketsParams{
		Status: strings.TrimSpace(q.Get("status")),
		Topic:  strings.TrimSpace(q.Get("topic")),
		Q:      strings.TrimSpace(q.Get("q")),
		Page:   parsePositiveInt(q.Get("page"), 1),
	}
	// Unknown values are dropped rather than failing the page.
	if !support.Status(p.Status).Valid() {
		p.Status = ""
	}
	if !support.Topic(p.Topic).Valid() {
		p.Topic = ""
	}
	return p
}

func (p ticketsParams) url(page int) string {
	v := url.Values{}
	for k, val := range map[string]string{"status": p.Status, "topic": p.Topic, "q": p.Q} {
		if val != "" {
			v.Set(k, val)
		}
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return ticketsListPath
	}
	return ticketsListPath + "?" + v.Encode()
}

func (h *handlers) ticketsListPage(w http.ResponseWriter, r *http.Request) {
	st, _ := staff.FromContext(r.Context())
	t := h.tr(r)
	p := parseTicketsParams(r.URL.Query())

	data := TicketsListData{
		Status: p.Status, Topic: p.Topic, Query: p.Q,
		Filtered: p.Status != "" || p.Topic != "" || p.Q != "",
		ResetURL: ticketsListPath,
	}
	data.StatusChips = h.ticketStatusChips(t, p, newTicketsFromWriter(w))
	data.TopicOptions = ticketTopicOptions(t, p.Topic)

	list, total, err := h.tickets.List(r.Context(), support.ListFilter{
		Status: support.Status(p.Status), Topic: support.Topic(p.Topic), Query: p.Q,
		Page: p.Page, PageSize: ticketsPageSize,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "admin: tickets list failed", "err", err)
		data.Err = t.T("admin.err.generic")
	}
	data.Rows = make([]TicketRowVM, 0, len(list))
	for _, s := range list {
		data.Rows = append(data.Rows, ticketRowVM(t, s))
	}
	data.Empty = len(data.Rows) == 0
	data.CountLabel = t.F("admin.tickets.count", total)
	data.HasPrev = p.Page > 1
	data.HasNext = p.Page*ticketsPageSize < total
	data.PrevURL, data.NextURL = p.url(p.Page-1), p.url(p.Page+1)

	page := h.shellPageData("tickets", t.T("admin.nav.tickets"), st)
	page.Toast = h.pageToast(r)
	page.Data = data
	if err := h.render.Render(w, "tickets", page); err != nil {
		slog.ErrorContext(r.Context(), "admin: tickets render failed", "err", err)
		http.Error(w, t.T("admin.err.render"), http.StatusInternalServerError)
	}
}

func (h *handlers) ticketStatusChips(t tr, p ticketsParams, newCount int) []StatusChipLink {
	chips := []StatusChipLink{{
		Label: t.T("admin.tickets.status_all"), Class: "admin-chip--all", Active: p.Status == "",
		URL: ticketsParams{Topic: p.Topic, Q: p.Q}.url(1),
	}}
	for _, s := range support.Statuses {
		key, class := ticketStatusMeta(s)
		chip := StatusChipLink{
			Label: t.T(key), Class: class, Active: p.Status == string(s),
			URL: ticketsParams{Status: string(s), Topic: p.Topic, Q: p.Q}.url(1),
		}
		if s == support.StatusNew {
			chip.Count = newCount
		}
		chips = append(chips, chip)
	}
	return chips
}

func ticketTopicOptions(t tr, selected string) []AuditOptionVM {
	opts := []AuditOptionVM{{Value: "", Label: t.T("admin.tickets.all_topics"), Selected: selected == ""}}
	for _, tp := range support.Topics {
		opts = append(opts, AuditOptionVM{Value: string(tp), Label: t.T(ticketTopicKey(tp)), Selected: selected == string(tp)})
	}
	return opts
}

func ticketRowVM(t tr, s support.Summary) TicketRowVM {
	key, class := ticketStatusMeta(s.Status)
	return TicketRowVM{
		ID: s.ID, URL: ticketsListPath + "/" + s.ID,
		NumberLabel:  "№" + strconv.FormatInt(s.Number, 10),
		DateLabel:    s.CreatedAt.In(reports.Location).Format(ticketDateLayout),
		Name:         s.Name,
		Phone:        displayPhone(s.Phone),
		Guest:        s.CustomerID == nil,
		TopicLabel:   t.T(ticketTopicKey(s.Topic)),
		ChannelLabel: t.T(ticketChannelKey(s.Channel)),
		StatusLabel:  t.T(key), StatusClass: class,
		Preview:   truncateRunes(s.LastMessage.Body, ticketPreviewLen),
		FromStaff: s.LastMessage.Author == support.AuthorStaff,
	}
}

// ---------- detail ----------

// TicketMessageVM is one message of the conversation.
type TicketMessageVM struct {
	FromStaff bool
	Author    string
	Text      string
	DateLabel string
}

// TicketStatusButton is one status-change button.
type TicketStatusButton struct {
	Value string
	Label string
	Class string
}

// TicketDetailData backs ticket_detail.gohtml.
type TicketDetailData struct {
	ID            string
	NumberLabel   string
	DateLabel     string
	StatusLabel   string
	StatusClass   string
	TopicLabel    string
	ChannelLabel  string
	Name          string
	Contact       PhoneContact
	Guest         bool
	OrderID       string
	OrderNumber   string
	Messages      []TicketMessageVM
	Closed        bool
	StatusButtons []TicketStatusButton
	MaxMessage    int
}

func (h *handlers) ticketDetailPage(w http.ResponseWriter, r *http.Request) {
	st, _ := staff.FromContext(r.Context())
	t := h.tr(r)
	tk, err := h.tickets.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.ticketError(w, r, err)
		return
	}
	page := h.shellPageData("tickets", t.F("admin.tickets.number", tk.Number), st)
	page.Screen = "ticket_detail"
	page.ShowBack = true
	page.BackURL = ticketsListPath
	page.Toast = h.pageToast(r)
	page.Data = ticketDetailVM(t, tk)
	if err := h.render.Render(w, "ticket_detail", page); err != nil {
		slog.ErrorContext(r.Context(), "admin: ticket render failed", "err", err)
		http.Error(w, t.T("admin.err.render"), http.StatusInternalServerError)
	}
}

// ticketError answers a failed lookup: 404 text for a missing request,
// 500 otherwise.
func (h *handlers) ticketError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apperr.AppError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		http.Error(w, h.tr(r).T("admin.apperr.ticket_not_found"), http.StatusNotFound)
		return
	}
	slog.ErrorContext(r.Context(), "admin: ticket load failed", "err", err)
	http.Error(w, h.tr(r).T("admin.err.generic"), http.StatusInternalServerError)
}

func ticketDetailVM(t tr, tk *support.Ticket) TicketDetailData {
	key, class := ticketStatusMeta(tk.Status)
	d := TicketDetailData{
		ID: tk.ID, NumberLabel: t.F("admin.tickets.number", tk.Number),
		DateLabel:   tk.CreatedAt.In(reports.Location).Format(ticketDateLayout),
		StatusLabel: t.T(key), StatusClass: class,
		TopicLabel:   t.T(ticketTopicKey(tk.Topic)),
		ChannelLabel: t.T(ticketChannelKey(tk.Channel)),
		Name:         tk.Name, Contact: phoneContact(tk.Phone), Guest: tk.IsGuest(),
		OrderNumber: tk.OrderNumber, Closed: tk.Status == support.StatusClosed,
		MaxMessage: support.MaxMessageLen,
	}
	if tk.OrderID != nil {
		d.OrderID = *tk.OrderID
	}
	for _, m := range tk.Messages {
		vm := TicketMessageVM{
			FromStaff: m.Author == support.AuthorStaff, Text: m.Body,
			DateLabel: m.CreatedAt.In(reports.Location).Format(ticketDateLayout),
		}
		switch {
		case !vm.FromStaff:
			vm.Author = t.T("admin.tickets.author_customer")
		case m.StaffName != "":
			vm.Author = m.StaffName
		default:
			vm.Author = t.T("admin.tickets.author_staff")
		}
		d.Messages = append(d.Messages, vm)
	}
	for _, s := range support.Statuses {
		if s == tk.Status {
			continue
		}
		key, class := ticketStatusMeta(s)
		d.StatusButtons = append(d.StatusButtons, TicketStatusButton{Value: string(s), Label: t.T(key), Class: class})
	}
	return d
}

func (h *handlers) ticketReply(w http.ResponseWriter, r *http.Request) {
	st, _ := staff.FromContext(r.Context())
	id := r.PathValue("id")
	detailURL := ticketsListPath + "/" + id
	if err := r.ParseForm(); err != nil {
		redirectWithToast(w, r, detailURL, toastKey("form_error"))
		return
	}
	staffID := ""
	if st != nil {
		staffID = st.ID
	}
	if _, err := h.tickets.Reply(r.Context(), id, staffID, r.PostFormValue("message")); err != nil {
		h.ticketWriteFailed(w, r, detailURL, err)
		return
	}
	redirectWithToast(w, r, detailURL, toastKey("ticket_replied"))
}

func (h *handlers) ticketStatusUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detailURL := ticketsListPath + "/" + id
	if err := r.ParseForm(); err != nil {
		redirectWithToast(w, r, detailURL, toastKey("form_error"))
		return
	}
	status := support.Status(r.PostFormValue("status"))
	tk, from, err := h.tickets.SetStatus(r.Context(), id, status)
	if err != nil {
		h.ticketWriteFailed(w, r, detailURL, err)
		return
	}
	if from != tk.Status {
		h.audit.Record(r.Context(), ticketStatusAuditEntry(tk, from))
	}
	redirectWithToast(w, r, detailURL, toastKey("ticket_status_"+string(tk.Status)))
}

// ticketWriteFailed: a missing request is a 404; a domain error comes
// back as a toast; anything else is logged and shown generically.
func (h *handlers) ticketWriteFailed(w http.ResponseWriter, r *http.Request, detailURL string, err error) {
	var ae *apperr.AppError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		h.ticketError(w, r, err)
		return
	}
	if !errors.As(err, &ae) || ae.Status >= http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "admin: ticket update failed", "err", err)
	}
	redirectWithToast(w, r, detailURL, toastForErr(err))
}

// ticketStatusAuditEntry journals a status change; the journal page
// renders from → to in the viewer's language (auditRowVM).
func ticketStatusAuditEntry(tk *support.Ticket, from support.Status) audit.Entry {
	args := audit.Args{"number": tk.Number}
	return audit.Entry{
		Action: audit.ActionTicketStatus, EntityType: audit.EntityTicket, EntityID: tk.ID,
		Summary: russianAuditSummary(audit.MsgTicketStatus, args, ""),
		MsgKey:  audit.MsgTicketStatus, MsgArgs: args,
		Details: map[string]any{"from": string(from), "to": string(tk.Status)},
	}
}

// ticketStatusLabel is a status's label for the journal ("—" if unknown).
func ticketStatusLabel(t tr, s string) string {
	if !support.Status(s).Valid() {
		return "—"
	}
	key, _ := ticketStatusMeta(support.Status(s))
	return t.T(key)
}

// ---------- new-requests badge ----------

// newTicketsCounter counts requests nobody has picked up yet.
type newTicketsCounter interface {
	CountNew(ctx context.Context) (int, error)
}

// ticketBadgeWriter carries the new-requests count to Render, keeping
// the orders count and language of the writer it wraps visible.
type ticketBadgeWriter struct {
	http.ResponseWriter
	newTickets int
}

func (bw *ticketBadgeWriter) adminNewTickets() int { return bw.newTickets }
func (bw *ticketBadgeWriter) adminNewOrders() int  { return newOrdersFromWriter(bw.ResponseWriter) }
func (bw *ticketBadgeWriter) adminLang() string    { return langFromWriter(bw.ResponseWriter) }

// Unwrap lets http.ResponseController reach the underlying writer.
func (bw *ticketBadgeWriter) Unwrap() http.ResponseWriter { return bw.ResponseWriter }

func newTicketsFromWriter(w http.ResponseWriter) int {
	if bw, ok := w.(interface{ adminNewTickets() int }); ok {
		return bw.adminNewTickets()
	}
	return 0
}

// withTicketBadge counts new requests for page views (GET/HEAD) of staff
// whose role sees Обращения; a failed count only drops the badge. Must
// run inside requireStaffRole.
func withTicketBadge(counter newTicketsCounter) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if (r.Method != http.MethodGet && r.Method != http.MethodHead) || counter == nil {
				next(w, r)
				return
			}
			st, ok := staff.FromContext(r.Context())
			if !ok || st == nil || !roleSeesNav("tickets", st.Role) {
				next(w, r)
				return
			}
			n, err := counter.CountNew(r.Context())
			if err != nil {
				slog.WarnContext(r.Context(), "admin nav: new tickets count failed", "err", err)
			}
			next(&ticketBadgeWriter{ResponseWriter: w, newTickets: n}, r)
		}
	}
}

// ticketsBadge handles GET /admin/tickets/badge: the polled sidebar badge.
func (h *handlers) ticketsBadge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := h.render.RenderFragment(w, "_tickets_badge", newTicketsFromWriter(w)); err != nil {
		slog.ErrorContext(r.Context(), "admin nav: tickets badge render failed", "err", err)
		http.Error(w, h.tr(r).T("admin.err.render"), http.StatusInternalServerError)
	}
}

// withTicketsBadge returns a copy of items with the «Обращения» entry
// carrying n.
func withTicketsBadge(items []NavItem, n int) []NavItem {
	out := make([]NavItem, len(items))
	copy(out, items)
	for i := range out {
		if out[i].Key == "tickets" {
			out[i].Badge = n
		}
	}
	return out
}
