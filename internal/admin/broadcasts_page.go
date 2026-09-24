// broadcasts_page.go is the "Рассылки" screen (W2 fix/promo-push): compose
// a promo push (title/text RU + KY, optional link to a product or a
// category), preview it, confirm, and queue it; the send itself runs in
// internal/broadcasts' background Worker. Below the form: the history with
// per-broadcast delivery stats, refreshed live while one is sending.
// Owner + manager (routes.go).
package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/broadcasts"
	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// historyLimit is how many past broadcasts the page lists.
const historyLimit = 50

// broadcastStore is the subset of *broadcasts.Repo the page uses.
type broadcastStore interface {
	Create(ctx context.Context, in broadcasts.Input, submitToken string, by broadcasts.Author) (string, bool, error)
	Get(ctx context.Context, id string) (*broadcasts.Broadcast, error)
	List(ctx context.Context, limit int) ([]broadcasts.Broadcast, error)
	Audience(ctx context.Context) (devices, customers int, err error)
	CategoryOptions(ctx context.Context) ([]broadcasts.Option, error)
	SearchProducts(ctx context.Context, query string) ([]broadcasts.Option, error)
}

type broadcastPages struct {
	h     *handlers
	store broadcastStore
	nudge func() // wakes the send worker; broadcasts.Nudge in production
}

// registerBroadcastRoutes mounts the Рассылки screen behind gate.
func registerBroadcastRoutes(mux *http.ServeMux, h *handlers, store broadcastStore, gate func(http.HandlerFunc) http.HandlerFunc) {
	p := &broadcastPages{h: h, store: store, nudge: broadcasts.Nudge}
	mux.HandleFunc("GET /admin/broadcasts", gate(p.page))
	mux.HandleFunc("POST /admin/broadcasts", gate(p.create))
	mux.HandleFunc("GET /admin/broadcasts/products", gate(p.searchProducts))
	mux.HandleFunc("GET /admin/broadcasts/{id}/stats", gate(p.stats))
}

// broadcastForm is the form's sticky state (re-rendered after an error).
type broadcastForm struct {
	TitleRU, BodyRU, TitleKY, BodyKY string
	LinkType                         string
	ProductID, ProductLabel          string
	CategoryID                       string
}

// broadcastRow is one history entry, pre-formatted for the template.
type broadcastRow struct {
	ID          string
	Title       string
	Body        string
	DateLabel   string
	Author      string
	LinkLabel   string
	StatusLabel string
	ChipClass   string
	Active      bool // queued/sending — the page polls its stats
	Targets     int
	Sent        int
	Failed      int
	Invalid     int
	Error       string
}

type broadcastsPageData struct {
	SubmitToken string
	Form        broadcastForm
	Error       string
	Devices     int
	Customers   int
	Categories  []broadcasts.Option
	History     []broadcastRow
	MaxTitle    int
	MaxBody     int
}

func statusView(status string) (label, chip string) {
	switch status {
	case broadcasts.StatusQueued:
		return "В очереди", "admin-chip--placed"
	case broadcasts.StatusSending:
		return "Отправляется", "admin-chip--confirmed"
	case broadcasts.StatusDone:
		return "Отправлена", "admin-chip--delivered"
	case broadcasts.StatusFailed:
		return "Ошибка", "admin-chip--cancelled"
	default:
		return status, "admin-chip--placed"
	}
}

func newBroadcastRow(b broadcasts.Broadcast) broadcastRow {
	label, chip := statusView(b.Status)
	row := broadcastRow{
		ID: b.ID, Title: b.TitleRU, Body: b.BodyRU,
		DateLabel: b.CreatedAt.In(reports.Location).Format("02.01.2006 15:04"),
		Author:    b.CreatedByName, LinkLabel: b.LinkLabel,
		StatusLabel: label, ChipClass: chip,
		Active:  b.Status == broadcasts.StatusQueued || b.Status == broadcasts.StatusSending,
		Targets: b.Targets, Sent: b.Sent, Failed: b.Failed, Invalid: b.InvalidRemoved,
	}
	if b.Error != nil {
		row.Error = *b.Error
	}
	return row
}

// newSubmitToken is the per-render idempotency token of the form.
func newSubmitToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(buf)
}

func (p *broadcastPages) page(w http.ResponseWriter, r *http.Request) {
	toast := ""
	if r.URL.Query().Get("queued") == "1" {
		toast = "Рассылка поставлена в очередь — статистика обновится ниже"
	}
	p.render(w, r, broadcastForm{}, newSubmitToken(), "", toast)
}

func (p *broadcastPages) render(w http.ResponseWriter, r *http.Request, form broadcastForm, token, errMsg, toast string) {
	st, _ := staff.FromContext(r.Context())
	ctx := r.Context()

	list, err := p.store.List(ctx, historyLimit)
	if err != nil {
		slog.ErrorContext(ctx, "admin: listing broadcasts failed", "err", err)
		http.Error(w, "не удалось загрузить рассылки", http.StatusInternalServerError)
		return
	}
	categories, err := p.store.CategoryOptions(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "admin: loading categories failed", "err", err)
		http.Error(w, "не удалось загрузить категории", http.StatusInternalServerError)
		return
	}
	devices, customers, err := p.store.Audience(ctx)
	if err != nil {
		// The count is informational — the page still works without it.
		slog.WarnContext(ctx, "admin: counting broadcast audience failed", "err", err)
	}

	rows := make([]broadcastRow, 0, len(list))
	for _, b := range list {
		rows = append(rows, newBroadcastRow(b))
	}
	data := p.h.shellPageData("broadcasts", "Рассылки", st)
	data.Toast = toast
	data.Data = broadcastsPageData{
		SubmitToken: token, Form: form, Error: errMsg,
		Devices: devices, Customers: customers,
		Categories: categories, History: rows,
		MaxTitle: broadcasts.MaxTitleLen, MaxBody: broadcasts.MaxBodyLen,
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	if err := p.h.render.Render(w, "broadcasts", data); err != nil {
		slog.ErrorContext(ctx, "admin: rendering broadcasts failed", "err", err)
	}
}

// create queues the broadcast (POST-redirect-GET). A resubmit of the same
// form (same submit_token) queues nothing new and lands on the same page.
func (p *broadcastPages) create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось прочитать форму", http.StatusBadRequest)
		return
	}
	form := broadcastForm{
		TitleRU: r.FormValue("title_ru"), BodyRU: r.FormValue("body_ru"),
		TitleKY: r.FormValue("title_ky"), BodyKY: r.FormValue("body_ky"),
		LinkType:  r.FormValue("link_type"),
		ProductID: r.FormValue("product_id"), ProductLabel: r.FormValue("product_label"),
		CategoryID: r.FormValue("category_id"),
	}
	in := broadcasts.Input{
		TitleRU: form.TitleRU, BodyRU: form.BodyRU, TitleKY: form.TitleKY, BodyKY: form.BodyKY,
		LinkType: form.LinkType,
	}
	switch form.LinkType {
	case broadcasts.LinkProduct:
		in.LinkID = form.ProductID
	case broadcasts.LinkCategory:
		in.LinkID = form.CategoryID
	}
	token := strings.TrimSpace(r.FormValue("submit_token"))

	st, _ := staff.FromContext(r.Context())
	var by broadcasts.Author
	if st != nil {
		by = broadcasts.Author{StaffID: st.ID, Name: st.Name}
	}
	id, created, err := p.store.Create(r.Context(), in, token, by)
	if err != nil {
		if token == "" {
			token = newSubmitToken()
		}
		p.render(w, r, form, token, appErrMessage(err), "")
		return
	}
	if created {
		slog.InfoContext(r.Context(), "admin: broadcast queued", "broadcast_id", id, "staff_id", by.StaffID)
		if p.nudge != nil {
			p.nudge()
		}
	}
	http.Redirect(w, r, "/admin/broadcasts?queued=1", http.StatusSeeOther)
}

// searchProducts backs the product picker: JSON [{id,label}].
func (p *broadcastPages) searchProducts(w http.ResponseWriter, r *http.Request) {
	opts, err := p.store.SearchProducts(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		slog.ErrorContext(r.Context(), "admin: broadcast product search failed", "err", err)
		http.Error(w, "ошибка поиска", http.StatusInternalServerError)
		return
	}
	writeBroadcastJSON(w, opts)
}

// broadcastStats is the live-refresh payload of one history row.
type broadcastStats struct {
	Status      string `json:"status"`
	StatusLabel string `json:"status_label"`
	ChipClass   string `json:"chip_class"`
	Active      bool   `json:"active"`
	Targets     int    `json:"targets"`
	Sent        int    `json:"sent"`
	Failed      int    `json:"failed"`
	Invalid     int    `json:"invalid_removed"`
}

func (p *broadcastPages) stats(w http.ResponseWriter, r *http.Request) {
	b, err := p.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, appErrMessage(err), http.StatusNotFound)
		return
	}
	row := newBroadcastRow(*b)
	writeBroadcastJSON(w, broadcastStats{
		Status: b.Status, StatusLabel: row.StatusLabel, ChipClass: row.ChipClass, Active: row.Active,
		Targets: b.Targets, Sent: b.Sent, Failed: b.Failed, Invalid: b.InvalidRemoved,
	})
}

func writeBroadcastJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
