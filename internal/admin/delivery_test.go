package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// fakeZones is an in-memory deliveryZoneStore.
type fakeZones struct {
	zones     map[string]*orders.DeliveryZone
	created   []orders.DeliveryZoneInput
	updated   map[string]orders.DeliveryZoneInput
	activeSet map[string]bool
	deleteErr error
	deleted   []string
}

func newFakeZones(zs ...orders.DeliveryZone) *fakeZones {
	f := &fakeZones{zones: map[string]*orders.DeliveryZone{}, updated: map[string]orders.DeliveryZoneInput{}, activeSet: map[string]bool{}}
	for i := range zs {
		z := zs[i]
		f.zones[z.ID] = &z
	}
	return f
}

func (f *fakeZones) ListAll(context.Context) ([]orders.DeliveryZone, error) {
	out := []orders.DeliveryZone{}
	for _, z := range f.zones {
		out = append(out, *z)
	}
	return out, nil
}

func (f *fakeZones) Get(_ context.Context, id string) (*orders.DeliveryZone, error) {
	if z, ok := f.zones[id]; ok {
		return z, nil
	}
	return nil, apperr.NotFound("delivery_zone_not_found", "зона доставки не найдена")
}

func (f *fakeZones) Create(_ context.Context, in orders.DeliveryZoneInput) (*orders.DeliveryZone, error) {
	if err := in.Normalize(); err != nil {
		return nil, err
	}
	f.created = append(f.created, in)
	return &orders.DeliveryZone{ID: "new"}, nil
}

func (f *fakeZones) Update(_ context.Context, id string, in orders.DeliveryZoneInput) (*orders.DeliveryZone, error) {
	if err := in.Normalize(); err != nil {
		return nil, err
	}
	f.updated[id] = in
	return &orders.DeliveryZone{ID: id}, nil
}

func (f *fakeZones) SetActive(_ context.Context, id string, active bool) error {
	f.activeSet[id] = active
	return nil
}

func (f *fakeZones) Delete(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func newDeliveryMux(t *testing.T, zones *fakeZones) *http.ServeMux {
	t.Helper()
	h := &handlers{render: newTestRenderer(t)}
	mux := http.NewServeMux()
	passthrough := func(next http.HandlerFunc) http.HandlerFunc { return next }
	registerDeliveryRoutes(mux, h, zones, passthrough)
	return mux
}

func ownerRequest(method, target string, form url.Values) *http.Request {
	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	owner := &staff.Staff{ID: "s1", Name: "Айгерим Б.", Role: staff.RoleOwner, IsActive: true}
	return r.WithContext(staff.NewContextWithStaff(r.Context(), owner))
}

func TestDeliveryPageListsZones(t *testing.T) {
	free := 5000.0
	zones := newFakeZones(
		orders.DeliveryZone{ID: "z1", NameRu: "Бишкек", NameKy: "Бишкек ш.", Fee: 200, FreeFrom: &free, IsActive: true},
		orders.DeliveryZone{ID: "z2", NameRu: "Пригород", NameKy: "Шаар четиндеги", Fee: 400.5},
	)
	rec := httptest.NewRecorder()
	newDeliveryMux(t, zones).ServeHTTP(rec, ownerRequest(http.MethodGet, "/admin/delivery", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, s := range []string{"Бишкек", "Шаар четиндеги", "бесплатно от 5000", "400.5 сом", "Деактивировать", "Активировать", `action="/admin/delivery/z1/delete"`, "Доставка"} {
		if !strings.Contains(body, s) {
			t.Errorf("page lacks %q", s)
		}
	}
}

func TestDeliveryPageEmptyExplainsFlatFee(t *testing.T) {
	rec := httptest.NewRecorder()
	newDeliveryMux(t, newFakeZones()).ServeHTTP(rec, ownerRequest(http.MethodGet, "/admin/delivery", nil))
	if !strings.Contains(rec.Body.String(), "DELIVERY_FEE_SOM") || !strings.Contains(rec.Body.String(), "Пока нет зон доставки") {
		t.Errorf("empty page should explain the flat fee: %s", rec.Body.String())
	}
}

func TestDeliveryCreateParsesForm(t *testing.T) {
	zones := newFakeZones()
	rec := httptest.NewRecorder()
	form := url.Values{"name_ru": {" Бишкек "}, "name_ky": {"Бишкек ш."}, "fee": {"1 500,50"}, "free_from": {"10000"}, "sort_order": {"2"}}
	newDeliveryMux(t, zones).ServeHTTP(rec, ownerRequest(http.MethodPost, "/admin/delivery", form))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(zones.created) != 1 {
		t.Fatalf("created = %v", zones.created)
	}
	in := zones.created[0]
	if in.NameRu != "Бишкек" || in.Fee != 1500.5 || in.FreeFrom == nil || *in.FreeFrom != 10000 || in.SortOrder != 2 || !in.IsActive {
		t.Errorf("input = %+v", in)
	}
}

func TestDeliveryCreateRejectsBadInput(t *testing.T) {
	for _, form := range []url.Values{
		{"name_ru": {"Б"}, "name_ky": {"Б"}, "fee": {""}},
		{"name_ru": {"Б"}, "name_ky": {"Б"}, "fee": {"abc"}},
		{"name_ru": {"Б"}, "name_ky": {"Б"}, "fee": {"-5"}},
		{"name_ru": {""}, "name_ky": {"Б"}, "fee": {"100"}},
		{"name_ru": {"Б"}, "name_ky": {"Б"}, "fee": {"100"}, "free_from": {"0"}},
		{"name_ru": {"Б"}, "name_ky": {"Б"}, "fee": {"NaN"}},
	} {
		zones := newFakeZones()
		rec := httptest.NewRecorder()
		newDeliveryMux(t, zones).ServeHTTP(rec, ownerRequest(http.MethodPost, "/admin/delivery", form))
		if rec.Code != http.StatusBadRequest || len(zones.created) != 0 || !strings.Contains(rec.Body.String(), "admin-form-error") {
			t.Errorf("form %v: status = %d, created = %v", form, rec.Code, zones.created)
		}
	}
}

func TestDeliveryUpdateKeepsActiveAndToggleFlips(t *testing.T) {
	zones := newFakeZones(orders.DeliveryZone{ID: "z1", NameRu: "Бишкек", NameKy: "Бишкек", Fee: 200, IsActive: false})
	mux := newDeliveryMux(t, zones)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, ownerRequest(http.MethodPost, "/admin/delivery/z1",
		url.Values{"name_ru": {"Бишкек-центр"}, "name_ky": {"Бишкек"}, "fee": {"250"}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d", rec.Code)
	}
	if in := zones.updated["z1"]; in.NameRu != "Бишкек-центр" || in.Fee != 250 || in.IsActive || in.FreeFrom != nil {
		t.Errorf("update input = %+v (active must stay as is)", in)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, ownerRequest(http.MethodPost, "/admin/delivery/z1/toggle", nil))
	if rec.Code != http.StatusSeeOther || !zones.activeSet["z1"] {
		t.Errorf("toggle: status = %d, active = %v", rec.Code, zones.activeSet)
	}
}

func TestDeliveryDeleteInUseShowsError(t *testing.T) {
	zones := newFakeZones(orders.DeliveryZone{ID: "z1", NameRu: "Бишкек", NameKy: "Бишкек", Fee: 200, IsActive: true})
	zones.deleteErr = apperr.Conflict("delivery_zone_in_use", "по этой зоне уже есть заказы — её можно только деактивировать")
	rec := httptest.NewRecorder()
	newDeliveryMux(t, zones).ServeHTTP(rec, ownerRequest(http.MethodPost, "/admin/delivery/z1/delete", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "можно только деактивировать") {
		t.Errorf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestDeliveryNavIsOwnerOnly(t *testing.T) {
	has := func(role staff.Role) bool {
		for _, it := range navItemsForRole(role, "orders") {
			if it.Key == "delivery" {
				return true
			}
		}
		return false
	}
	if !has(staff.RoleOwner) || has(staff.RoleManager) || has(staff.RolePointStaff) {
		t.Error("Доставка must be in the owner's sidebar only")
	}
}
