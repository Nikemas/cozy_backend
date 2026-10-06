package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

func TestProductsPageURLKeepsFiltersAndDropsStaleParams(t *testing.T) {
	q := url.Values{
		"cat": {"men"}, "sub": {"men-classic"}, "q": {"кеды"}, "point": {"p1"},
		"oos": {"1"}, "page_size": {"50"}, "page": {"2"}, "toast": {"Сохранено"}, "bulk_fail": {"x"},
	}

	next := productsPageURL(q, 3)
	u, err := url.Parse(next)
	if err != nil {
		t.Fatalf("parse %q: %v", next, err)
	}
	got := u.Query()
	for _, k := range []string{"cat", "sub", "q", "point", "oos", "page_size"} {
		if got.Get(k) != q.Get(k) {
			t.Errorf("%s = %q, want %q (url %s)", k, got.Get(k), q.Get(k), next)
		}
	}
	if got.Get("page") != "3" {
		t.Errorf("page = %q, want 3", got.Get("page"))
	}
	if got.Has("toast") || got.Has("bulk_fail") {
		t.Errorf("stale params kept: %s", next)
	}
	if u.Path != "/admin/products" {
		t.Errorf("path = %q", u.Path)
	}
	if q.Get("page") != "2" {
		t.Error("input query was mutated")
	}
}

func TestProductsPageURLFirstPageOmitsPage(t *testing.T) {
	got := productsPageURL(url.Values{"page": {"2"}, "cat": {"men"}}, 1)
	if got != "/admin/products?cat=men" {
		t.Errorf("got %q", got)
	}
	if got := productsPageURL(url.Values{}, 1); got != "/admin/products" {
		t.Errorf("no filters: got %q", got)
	}
}

type fakeNewOrders struct {
	n       int
	err     error
	calls   int
	pointID *string
}

func (f *fakeNewOrders) CountNewOrders(_ context.Context, pointID *string) (int, error) {
	f.calls++
	f.pointID = pointID
	return f.n, f.err
}

// runWithBadges runs a handler that renders the orders screen behind
// withNavBadges, as staff st, and returns the response.
func runWithBadges(t *testing.T, counter *fakeNewOrders, st *staff.Staff, method string) *httptest.ResponseRecorder {
	t.Helper()
	rr := newTestRenderer(t)
	h := &handlers{render: rr}
	page := func(w http.ResponseWriter, r *http.Request) {
		pd := h.shellPageData("products", "admin.nav.products", st)
		if err := rr.Render(w, "products", pd); err != nil {
			t.Fatalf("render: %v", err)
		}
	}
	req := httptest.NewRequest(method, "/admin/products", nil)
	req = req.WithContext(staff.NewContextWithStaff(req.Context(), st))
	rec := httptest.NewRecorder()
	withNavBadges(counter)(page)(withLang(rec, req), req)
	return rec
}

func TestNavBadgeShowsNewOrdersCount(t *testing.T) {
	counter := &fakeNewOrders{n: 3}
	owner := &staff.Staff{ID: "s1", Name: "Owner", Role: staff.RoleOwner, IsActive: true}

	body := runWithBadges(t, counter, owner, http.MethodGet).Body.String()

	if counter.calls != 1 || counter.pointID != nil {
		t.Fatalf("calls=%d pointID=%v, want 1 call for all points", counter.calls, counter.pointID)
	}
	if !strings.Contains(body, `class="admin-nav__badge"`) || !strings.Contains(body, ">3</span>") {
		t.Errorf("sidebar has no orders badge with 3:\n%s", body)
	}
	if !strings.Contains(body, `admin-header__hamburger-badge`) {
		t.Error("mobile hamburger has no new-orders dot")
	}
}

func TestNavBadgeHiddenWhenNoNewOrders(t *testing.T) {
	owner := &staff.Staff{ID: "s1", Name: "Owner", Role: staff.RoleOwner, IsActive: true}
	body := runWithBadges(t, &fakeNewOrders{n: 0}, owner, http.MethodGet).Body.String()
	if strings.Contains(body, `class="admin-nav__badge"`) || strings.Contains(body, "admin-header__hamburger-badge") {
		t.Error("badge rendered for zero new orders")
	}
}

func TestNavBadgeScopedToPointStaffPoint(t *testing.T) {
	pt := "pt-1"
	counter := &fakeNewOrders{n: 1}
	ps := &staff.Staff{ID: "s2", Name: "Seller", Role: staff.RolePointStaff, PointID: &pt, IsActive: true}
	runWithBadges(t, counter, ps, http.MethodGet)
	if counter.pointID == nil || *counter.pointID != pt {
		t.Fatalf("point_staff count not scoped to own point: %v", counter.pointID)
	}

	noPoint := &staff.Staff{ID: "s3", Name: "Seller", Role: staff.RolePointStaff, IsActive: true}
	counter2 := &fakeNewOrders{n: 9}
	body := runWithBadges(t, counter2, noPoint, http.MethodGet).Body.String()
	if counter2.calls != 0 || strings.Contains(body, `class="admin-nav__badge"`) {
		t.Error("point_staff without a point must not see a count")
	}
}

func TestNavBadgeSkippedForPostAndOnError(t *testing.T) {
	owner := &staff.Staff{ID: "s1", Name: "Owner", Role: staff.RoleOwner, IsActive: true}
	counter := &fakeNewOrders{n: 5}
	runWithBadges(t, counter, owner, http.MethodPost)
	if counter.calls != 0 {
		t.Error("POST must not run the count query")
	}

	failing := &fakeNewOrders{n: 5, err: errors.New("db down")}
	rec := runWithBadges(t, failing, owner, http.MethodGet)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `class="admin-nav__badge"`) {
		t.Errorf("count failure must degrade to no badge, got %d", rec.Code)
	}
}

func TestNavBadgeKeepsAdminLanguage(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/orders", nil)
	req.AddCookie(&http.Cookie{Name: langCookieName, Value: "ky"})
	rec := httptest.NewRecorder()
	var lang string
	withNavBadges(&fakeNewOrders{})(func(w http.ResponseWriter, r *http.Request) {
		lang = langFromWriter(w)
	})(withLang(rec, req), req)
	if lang != "ky" {
		t.Errorf("lang through badge writer = %q, want ky", lang)
	}
}

func TestCountNewOrdersQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	repo := orderBadgeRepo{db: db}

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM orders WHERE status = 'placed'$`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
	n, err := repo.CountNewOrders(context.Background(), nil)
	if err != nil || n != 4 {
		t.Fatalf("all points: n=%d err=%v", n, err)
	}

	pt := "pt-1"
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM orders WHERE status = 'placed' AND point_id = \$1`).
		WithArgs(pt).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	n, err = repo.CountNewOrders(context.Background(), &pt)
	if err != nil || n != 2 {
		t.Fatalf("one point: n=%d err=%v", n, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestWithPlacedCountOnlyMarksPlacedChip(t *testing.T) {
	chips := []StatusChipLink{{Label: "Все", Class: "admin-chip--all"}, {Label: "Оформлен", Class: "admin-chip--placed"}}
	got := withPlacedCount(chips, 4)
	if got[0].Count != 0 || got[1].Count != 4 {
		t.Errorf("counts = %d,%d", got[0].Count, got[1].Count)
	}
	if chips[1].Count != 0 {
		t.Error("input chips mutated")
	}
}

func TestPhoneContact(t *testing.T) {
	cases := []struct {
		in                     string
		display, tel, whatsapp string
	}{
		{"+996700111222", "+996 700 111 222", "tel:+996700111222", "https://wa.me/996700111222"},
		{"996 555 12-34-56", "+996 555 123 456", "tel:+996555123456", "https://wa.me/996555123456"},
		{"+7 701 000 00 00", "+7 701 000 00 00", "tel:+77010000000", "https://wa.me/77010000000"},
		{"", "", "", ""},
	}
	for _, c := range cases {
		got := phoneContact(c.in)
		if got.Display != c.display || string(got.Tel) != c.tel || got.WhatsApp != c.whatsapp {
			t.Errorf("phoneContact(%q) = %+v, want %q %q %q", c.in, got, c.display, c.tel, c.whatsapp)
		}
	}
}

func TestBuildStatusButtonsUseActionLabels(t *testing.T) {
	want := map[string]string{
		"confirmed":        "Подтвердить заказ",
		"courier_assigned": "Передать курьеру",
		"delivered":        "Отметить доставленным",
		"cancelled":        "Отменить заказ",
	}
	for _, from := range []orders.OrderStatus{orders.StatusPlaced, orders.StatusConfirmed, orders.StatusCourierAssigned} {
		for _, b := range buildStatusButtons(ruTr, from, staff.RoleOwner) {
			if b.Label != want[b.Value] {
				t.Errorf("button %s label = %q, want %q", b.Value, b.Label, want[b.Value])
			}
		}
	}
}

func renderOrderDetailUX(t *testing.T, d OrderDetailData, toast string) string {
	t.Helper()
	rr := newTestRenderer(t)
	owner := &staff.Staff{ID: "s1", Name: "Owner", Role: staff.RoleOwner, IsActive: true}
	h := &handlers{render: rr}
	pd := h.shellPageData("orders", "Заказ", owner)
	pd.ShowBack = true
	pd.BackURL = "/admin/orders"
	pd.Toast = toast
	pd.Data = d
	rec := httptest.NewRecorder()
	if err := rr.Render(rec, "order_detail", pd); err != nil {
		t.Fatalf("render: %v", err)
	}
	return rec.Body.String()
}

func TestOrderDetailPhoneIsCallableAndCopyable(t *testing.T) {
	body := renderOrderDetailUX(t, OrderDetailData{
		ID: "o1", Number: "COZY-1", CustomerName: "Айбек",
		Contact:       phoneContact("+996700111222"),
		StatusButtons: buildStatusButtons(ruTr, orders.StatusCourierAssigned, staff.RoleOwner),
	}, "")
	for _, want := range []string{
		`href="tel:&#43;996700111222"`,
		`href="https://wa.me/996700111222"`,
		`data-copy="&#43;996700111222"`,
		"&#43;996 700 111 222",
		"Айбек",
		// delivered is terminal: it asks first, like cancel.
		`data-confirm-title="Заказ доставлен?"`,
		`href="/admin/orders"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("order detail missing %q", want)
		}
	}
}

func TestOrderStatusUpdateSuccessShowsToast(t *testing.T) {
	svc := &fakeOrdersSvc{order: &orders.Order{ID: "o1", Status: orders.StatusConfirmed}}
	h := &handlers{ordersSvc: svc}
	owner := &staff.Staff{ID: "s1", Role: staff.RoleOwner}
	w := httptest.NewRecorder()
	r := requestAs(http.MethodPost, "/admin/orders/o1/status", owner, url.Values{"status": {"confirmed"}})
	r.SetPathValue("id", "o1")

	h.orderStatusUpdate(w, r)

	loc := w.Header().Get("Location")
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/admin/orders/o1?toast=") {
		t.Fatalf("got %d %q, want redirect to detail with a toast", w.Code, loc)
	}
	u, _ := url.Parse(loc)
	if got := u.Query().Get("toast"); !strings.Contains(got, "Подтверждён") {
		t.Errorf("toast = %q, want the new status", got)
	}
}

func TestOrderDetailShowsToastParam(t *testing.T) {
	body := renderOrderDetailUX(t, OrderDetailData{ID: "o1", Number: "COZY-1"}, "Статус изменён")
	if !strings.Contains(body, "Статус изменён") {
		t.Error("toast not rendered")
	}
}
