package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/broadcasts"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

type fakeBroadcastStore struct {
	created   []broadcasts.Input
	tokens    []string
	author    broadcasts.Author
	createErr error
	dupe      bool
	list      []broadcasts.Broadcast
	search    string
}

func (f *fakeBroadcastStore) Create(_ context.Context, in broadcasts.Input, token string, by broadcasts.Author) (string, bool, error) {
	if f.createErr != nil {
		return "", false, f.createErr
	}
	f.created = append(f.created, in)
	f.tokens = append(f.tokens, token)
	f.author = by
	return "b1", !f.dupe, nil
}

func (f *fakeBroadcastStore) Get(_ context.Context, id string) (*broadcasts.Broadcast, error) {
	for _, b := range f.list {
		if b.ID == id {
			return &b, nil
		}
	}
	return nil, apperr.NotFound("broadcast_not_found", "рассылка не найдена")
}

func (f *fakeBroadcastStore) List(context.Context, int) ([]broadcasts.Broadcast, error) {
	return f.list, nil
}

func (f *fakeBroadcastStore) Audience(context.Context) (int, int, error) { return 12, 9, nil }

func (f *fakeBroadcastStore) CategoryOptions(context.Context) ([]broadcasts.Option, error) {
	return []broadcasts.Option{{ID: "c1", Label: "Обувь / Кроссовки"}}, nil
}

func (f *fakeBroadcastStore) SearchProducts(_ context.Context, q string) ([]broadcasts.Option, error) {
	f.search = q
	return []broadcasts.Option{{ID: "p1", Label: "Air Max · Nike"}}, nil
}

func manager() *staff.Staff {
	return &staff.Staff{ID: "m1", Name: "Айгерим Б.", Role: staff.RoleManager, IsActive: true}
}

func newBroadcastPages(t *testing.T, store *fakeBroadcastStore) (*broadcastPages, *int) {
	nudges := 0
	return &broadcastPages{
		h:     &handlers{render: newTestRenderer(t)},
		store: store,
		nudge: func() { nudges++ },
	}, &nudges
}

func TestBroadcastsPageRendersFormAudienceAndHistory(t *testing.T) {
	errMsg := "loading targets: db down"
	store := &fakeBroadcastStore{list: []broadcasts.Broadcast{
		{ID: "b1", TitleRU: "Скидки −20%", Status: broadcasts.StatusSending, CreatedByName: "Айгерим Б.",
			LinkLabel: "Air Max", Targets: 12, Sent: 5, CreatedAt: time.Now()},
		{ID: "b2", TitleRU: "Новинки", Status: broadcasts.StatusFailed, Error: &errMsg, CreatedAt: time.Now()},
	}}
	p, _ := newBroadcastPages(t, store)
	w := httptest.NewRecorder()
	p.page(w, requestAs(http.MethodGet, "/admin/broadcasts?queued=1", manager(), nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`name="submit_token" value="`, `name="title_ru"`, `name="title_ky"`, `name="body_ky"`,
		`data-confirm="Push получат 12 устр. (9 покупателей)`, `Обувь / Кроссовки`,
		`Скидки −20%`, `data-bc-active="1"`, `Отправляется`, `ссылка: Air Max`, `db down`,
		`Рассылка поставлена в очередь`, `href="/admin/broadcasts"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestBroadcastCreateQueuesNudgesAndRedirects(t *testing.T) {
	store := &fakeBroadcastStore{}
	p, nudges := newBroadcastPages(t, store)
	form := url.Values{
		"submit_token": {"tok-1"}, "title_ru": {"Скидки"}, "body_ru": {"До воскресенья"},
		"link_type": {"category"}, "category_id": {"c1"}, "product_id": {"ignored"},
	}
	w := httptest.NewRecorder()
	p.create(w, requestAs(http.MethodPost, "/admin/broadcasts", manager(), form))

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/broadcasts?queued=1" {
		t.Fatalf("status %d location %q", w.Code, w.Header().Get("Location"))
	}
	if len(store.created) != 1 || store.created[0].LinkType != "category" || store.created[0].LinkID != "c1" {
		t.Errorf("created = %+v", store.created)
	}
	if store.tokens[0] != "tok-1" || store.author.StaffID != "m1" || store.author.Name != "Айгерим Б." {
		t.Errorf("token %q author %+v", store.tokens[0], store.author)
	}
	if *nudges != 1 {
		t.Errorf("nudges = %d, want 1", *nudges)
	}

	// Same form resubmitted: store reports it as a duplicate → no nudge.
	store.dupe = true
	w = httptest.NewRecorder()
	p.create(w, requestAs(http.MethodPost, "/admin/broadcasts", manager(), form))
	if w.Code != http.StatusSeeOther || *nudges != 1 {
		t.Errorf("resubmit: status %d nudges %d; want redirect and no second nudge", w.Code, *nudges)
	}
}

func TestBroadcastCreateValidationErrorKeepsForm(t *testing.T) {
	store := &fakeBroadcastStore{createErr: apperr.BadRequest("invalid_broadcast_link", "товар не найден или скрыт")}
	p, nudges := newBroadcastPages(t, store)
	form := url.Values{
		"submit_token": {"tok-1"}, "title_ru": {"Скидки на Air"}, "body_ru": {"b"},
		"link_type": {"product"}, "product_id": {"p1"}, "product_label": {"Air Max · Nike"},
	}
	w := httptest.NewRecorder()
	p.create(w, requestAs(http.MethodPost, "/admin/broadcasts", manager(), form))

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"товар не найден или скрыт", `value="Скидки на Air"`, `value="tok-1"`, "Выбран: Air Max · Nike"} {
		if !strings.Contains(body, want) {
			t.Errorf("re-rendered form missing %q", want)
		}
	}
	if *nudges != 0 {
		t.Error("failed create must not nudge the worker")
	}
}

func TestBroadcastStatsAndProductSearchJSON(t *testing.T) {
	store := &fakeBroadcastStore{list: []broadcasts.Broadcast{{ID: "b1", Status: broadcasts.StatusDone, Targets: 3, Sent: 2, InvalidRemoved: 1}}}
	p, _ := newBroadcastPages(t, store)

	r := requestAs(http.MethodGet, "/admin/broadcasts/b1/stats", manager(), nil)
	r.SetPathValue("id", "b1")
	w := httptest.NewRecorder()
	p.stats(w, r)
	if got := w.Body.String(); !strings.Contains(got, `"status":"done"`) || !strings.Contains(got, `"active":false`) ||
		!strings.Contains(got, `"sent":2`) || !strings.Contains(got, `"invalid_removed":1`) {
		t.Errorf("stats = %s", got)
	}

	r = requestAs(http.MethodGet, "/admin/broadcasts/nope/stats", manager(), nil)
	r.SetPathValue("id", "nope")
	w = httptest.NewRecorder()
	p.stats(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown id: status %d", w.Code)
	}

	w = httptest.NewRecorder()
	p.searchProducts(w, requestAs(http.MethodGet, "/admin/broadcasts/products?q=air", manager(), nil))
	if store.search != "air" || !strings.Contains(w.Body.String(), `"label":"Air Max · Nike"`) {
		t.Errorf("search q=%q body=%s", store.search, w.Body.String())
	}
}

func TestBroadcastRoutesAreOwnerOrManagerOnly(t *testing.T) {
	mux := http.NewServeMux()
	resolver := &fakeResolver{st: pointStaff(strPtr("pt1"))}
	gate := requireStaffRole(resolver, staff.RoleOwner, staff.RoleManager)
	registerBroadcastRoutes(mux, &handlers{render: newTestRenderer(t)}, &fakeBroadcastStore{}, gate)

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/admin/broadcasts", nil),
		httptest.NewRequest(http.MethodPost, "/admin/broadcasts", strings.NewReader("title_ru=x")),
		httptest.NewRequest(http.MethodGet, "/admin/broadcasts/products?q=air", nil),
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") == "/admin/broadcasts" {
			t.Errorf("%s %s as point_staff: status %d → %q, want redirect away", req.Method, req.URL, w.Code, w.Header().Get("Location"))
		}
	}
}
