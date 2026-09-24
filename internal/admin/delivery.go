// delivery.go implements the "Доставка" screen (GET /admin/delivery,
// owner only): delivery zones with their fee and free-delivery threshold
// (orders.DeliveryZone, migration 000035) — create/edit in a modal,
// activate/deactivate, delete (only while no order uses the zone). While
// no zone is active, delivery costs the flat DELIVERY_FEE_SOM.
package admin

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// deliveryZoneStore is the subset of *orders.DeliveryZoneRepo the page
// needs (a fake in tests).
type deliveryZoneStore interface {
	ListAll(ctx context.Context) ([]orders.DeliveryZone, error)
	Get(ctx context.Context, id string) (*orders.DeliveryZone, error)
	Create(ctx context.Context, in orders.DeliveryZoneInput) (*orders.DeliveryZone, error)
	Update(ctx context.Context, id string, in orders.DeliveryZoneInput) (*orders.DeliveryZone, error)
	SetActive(ctx context.Context, id string, active bool) error
	Delete(ctx context.Context, id string) error
}

// deliveryPage serves the screen; h supplies rendering and the shell.
type deliveryPage struct {
	h     *handlers
	zones deliveryZoneStore
}

// registerDeliveryRoutes mounts the "Доставка" screen, owner only.
func registerDeliveryRoutes(mux *http.ServeMux, h *handlers, zones deliveryZoneStore, ownerOnly func(http.HandlerFunc) http.HandlerFunc) {
	p := &deliveryPage{h: h, zones: zones}
	mux.HandleFunc("GET /admin/delivery", ownerOnly(p.list))
	mux.HandleFunc("POST /admin/delivery", ownerOnly(p.create))
	mux.HandleFunc("POST /admin/delivery/{id}", ownerOnly(p.update))
	mux.HandleFunc("POST /admin/delivery/{id}/toggle", ownerOnly(p.toggle))
	mux.HandleFunc("POST /admin/delivery/{id}/delete", ownerOnly(p.remove))
}

// deliveryZoneRow is one pre-formatted row of delivery.gohtml.
type deliveryZoneRow struct {
	ID          string
	NameRu      string
	NameKy      string
	Fee         string // "200"
	FreeFrom    string // "5000" or ""
	SortOrder   int
	IsActive    bool
	ChipLabel   string
	ChipClass   string
	ToggleLabel string
}

// deliveryPageData is delivery.gohtml's PageData.Data.
type deliveryPageData struct {
	Rows []deliveryZoneRow
	// FlatFee is DELIVERY_FEE_SOM — what delivery costs while no zone is
	// active.
	FlatFee   string
	AnyActive bool
	Error     string
}

func zoneSom(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func newDeliveryZoneRow(z orders.DeliveryZone) deliveryZoneRow {
	row := deliveryZoneRow{ID: z.ID, NameRu: z.NameRu, NameKy: z.NameKy, Fee: zoneSom(z.Fee), SortOrder: z.SortOrder, IsActive: z.IsActive}
	if z.FreeFrom != nil {
		row.FreeFrom = zoneSom(*z.FreeFrom)
	}
	if z.IsActive {
		row.ChipLabel, row.ChipClass, row.ToggleLabel = "Активна", "admin-chip--active", "Деактивировать"
	} else {
		row.ChipLabel, row.ChipClass, row.ToggleLabel = "Неактивна", "admin-chip--inactive", "Активировать"
	}
	return row
}

func (p *deliveryPage) list(w http.ResponseWriter, r *http.Request) {
	p.render(w, r, "")
}

func (p *deliveryPage) render(w http.ResponseWriter, r *http.Request, errMsg string) {
	st, _ := staff.FromContext(r.Context())
	zones, err := p.zones.ListAll(r.Context())
	if err != nil {
		http.Error(w, "не удалось загрузить зоны доставки", http.StatusInternalServerError)
		return
	}
	data := deliveryPageData{Error: errMsg, FlatFee: zoneSom(orders.CurrentSettings().DeliveryFee)}
	for _, z := range zones {
		data.Rows = append(data.Rows, newDeliveryZoneRow(z))
		data.AnyActive = data.AnyActive || z.IsActive
	}
	page := p.h.shellPageData("delivery", "Доставка", st)
	page.Data = data
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	if err := p.h.render.Render(w, "delivery", page); err != nil {
		http.Error(w, "ошибка рендеринга страницы", http.StatusInternalServerError)
	}
}

// parseSom reads a money field ("1 500", "1500,50"); empty is ok=false.
func parseSom(s string) (v float64, present bool, err error) {
	s = strings.NewReplacer(" ", "", " ", "", ",", ".").Replace(strings.TrimSpace(s))
	if s == "" {
		return 0, false, nil
	}
	v, err = strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, true, apperr.BadRequest("invalid_delivery_zone_input", "сумма должна быть числом")
	}
	return v, true, nil
}

// parseZoneForm reads the modal's fields; active is the zone's current
// state (a new zone starts active).
func parseZoneForm(r *http.Request, active bool) (orders.DeliveryZoneInput, error) {
	in := orders.DeliveryZoneInput{
		NameRu:   r.FormValue("name_ru"),
		NameKy:   r.FormValue("name_ky"),
		IsActive: active,
	}
	fee, ok, err := parseSom(r.FormValue("fee"))
	if err != nil {
		return in, err
	}
	if !ok {
		return in, apperr.BadRequest("invalid_delivery_zone_input", "укажите стоимость доставки")
	}
	in.Fee = fee
	freeFrom, ok, err := parseSom(r.FormValue("free_from"))
	if err != nil {
		return in, err
	}
	if ok {
		in.FreeFrom = &freeFrom
	}
	if s := strings.TrimSpace(r.FormValue("sort_order")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < -1000000 || n > 1000000 {
			return in, apperr.BadRequest("invalid_delivery_zone_input", "порядок — целое число")
		}
		in.SortOrder = n
	}
	return in, nil
}

func (p *deliveryPage) create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		p.render(w, r, "не удалось прочитать форму")
		return
	}
	in, err := parseZoneForm(r, true)
	if err == nil {
		_, err = p.zones.Create(r.Context(), in)
	}
	if err != nil {
		p.render(w, r, appErrMessage(err))
		return
	}
	http.Redirect(w, r, "/admin/delivery", http.StatusSeeOther)
}

func (p *deliveryPage) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		p.render(w, r, "не удалось прочитать форму")
		return
	}
	current, err := p.zones.Get(r.Context(), id)
	if err != nil {
		p.render(w, r, appErrMessage(err))
		return
	}
	in, err := parseZoneForm(r, current.IsActive)
	if err == nil {
		_, err = p.zones.Update(r.Context(), id, in)
	}
	if err != nil {
		p.render(w, r, appErrMessage(err))
		return
	}
	http.Redirect(w, r, "/admin/delivery", http.StatusSeeOther)
}

func (p *deliveryPage) toggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	current, err := p.zones.Get(r.Context(), id)
	if err == nil {
		err = p.zones.SetActive(r.Context(), id, !current.IsActive)
	}
	if err != nil {
		p.render(w, r, appErrMessage(err))
		return
	}
	http.Redirect(w, r, "/admin/delivery", http.StatusSeeOther)
}

func (p *deliveryPage) remove(w http.ResponseWriter, r *http.Request) {
	if err := p.zones.Delete(r.Context(), r.PathValue("id")); err != nil {
		p.render(w, r, appErrMessage(err))
		return
	}
	http.Redirect(w, r, "/admin/delivery", http.StatusSeeOther)
}
