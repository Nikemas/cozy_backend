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
	if !strings.Contains(body, `data-count="0" hidden`) || !strings.Contains(body, `class="admin-header__hamburger-badge" aria-hidden="true" hidden`) {
		t.Error("badge shown for zero new orders")
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
	if counter2.calls != 0 || !strings.Contains(body, `data-count="0" hidden`) {
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
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-count="0" hidden`) {
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

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM orders WHERE status = 'placed' AND \(payment_method <> 'online_card' OR payment_status = 'paid'\)$`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
	n, err := repo.CountNewOrders(context.Background(), nil)
	if err != nil || n != 4 {
		t.Fatalf("all points: n=%d err=%v", n, err)
	}

	pt := "pt-1"
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM orders WHERE status = 'placed' AND .* AND point_id = \$1`).
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
	if got := toastFromQuery(ruTr, u.Query()); !strings.Contains(got, "Подтверждён") {
		t.Errorf("toast = %q, want the new status", got)
	}
}

func TestOrderDetailShowsToastParam(t *testing.T) {
	body := renderOrderDetailUX(t, OrderDetailData{ID: "o1", Number: "COZY-1"}, "Статус изменён")
	if !strings.Contains(body, "Статус изменён") {
		t.Error("toast not rendered")
	}
}

func postForm(target string, st *staff.Staff, body url.Values) *http.Request {
	return requestAs(http.MethodPost, target, st, body)
}

func TestFormRetryForPointsKeepsTypedValues(t *testing.T) {
	r := postForm("/admin/points/p1", nil, url.Values{"name": {"ЦУМ"}, "city": {"Ош"}, "latitude": {"abc"}, "longitude": {"1"}})
	_ = r.ParseForm()
	got := pointsFormRetry(r, ruTr)
	if got == nil || got.Modal != "admin-point-modal" || got.Action != "/admin/points/p1" {
		t.Fatalf("retry = %+v", got)
	}
	if got.Values["name"] != "ЦУМ" || got.Values["latitude"] != "abc" || got.Values["city"] != "Ош" {
		t.Errorf("values = %v", got.Values)
	}
	if got.Title != ruTr.T("admin.points.edit_title") {
		t.Errorf("title = %q", got.Title)
	}
	toggle := postForm("/admin/points/p1/toggle", nil, url.Values{})
	_ = toggle.ParseForm()
	if pointsFormRetry(toggle, ruTr) != nil {
		t.Error("toggle failure must not reopen the form")
	}
	get := httptest.NewRequest(http.MethodGet, "/admin/points", nil)
	if pointsFormRetry(get, ruTr) != nil {
		t.Error("GET must not reopen the form")
	}
}

func TestStaffCreateErrorReopensModalWithoutPassword(t *testing.T) {
	r := postForm("/admin/staff", nil, url.Values{"name": {"Айгерим"}, "phone": {"+996555000777"}, "role": {"point_staff"}, "point_id": {"pt1"}, "password": {"secret-123456"}})
	_ = r.ParseForm()
	got := staffFormRetry(r)
	if got == nil || got.Modal != "admin-staff-modal" {
		t.Fatalf("retry = %+v", got)
	}
	if _, ok := got.Values["password"]; ok {
		t.Error("password must never be echoed back into the page")
	}
	if got.Values["phone"] != "+996555000777" || got.Values["role"] != "point_staff" || got.Values["point_id"] != "pt1" {
		t.Errorf("values = %v", got.Values)
	}
}

func TestCategoriesFormRetryPicksModal(t *testing.T) {
	create := postForm("/admin/categories", nil, url.Values{"name_ru": {"Кеды"}, "slug": {"keds"}})
	_ = create.ParseForm()
	if got := categoriesFormRetry(create); got == nil || got.Modal != "admin-category-create-modal" || got.Values["slug"] != "keds" {
		t.Errorf("create retry = %+v", got)
	}
	edit := postForm("/admin/categories/c1", nil, url.Values{"name_ru": {"Кеды"}})
	_ = edit.ParseForm()
	if got := categoriesFormRetry(edit); got == nil || got.Modal != "admin-category-edit-modal" || got.Action != "/admin/categories/c1" {
		t.Errorf("edit retry = %+v", got)
	}
	del := postForm("/admin/categories/c1/delete", nil, url.Values{})
	_ = del.ParseForm()
	if categoriesFormRetry(del) != nil {
		t.Error("delete failure must not reopen a form")
	}
}

func TestFormRetryRenderedAsJSON(t *testing.T) {
	rr := newTestRenderer(t)
	owner := &staff.Staff{ID: "s1", Name: "Owner", Role: staff.RoleOwner, IsActive: true}
	h := &handlers{render: rr}
	pd := h.shellPageData("points", "admin.nav.points", owner)
	pd.FormRetry = &FormRetry{Modal: "admin-point-modal", Action: "/admin/points", Values: map[string]string{"name": `</script><b>"x"`}}
	rec := httptest.NewRecorder()
	if err := rr.Render(rec, "points", pd); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="admin-form-retry"`) || !strings.Contains(body, `"modal":"admin-point-modal"`) {
		t.Errorf("retry JSON missing:\n%s", body)
	}
	if strings.Contains(body, `</script><b>`) {
		t.Error("retry values not escaped inside <script>")
	}
}

func TestWithRetryErrorCopies(t *testing.T) {
	if withRetryError(nil, "x") != nil {
		t.Error("nil retry must stay nil")
	}
	in := &FormRetry{Modal: "m"}
	out := withRetryError(in, "занято")
	if out.Error != "занято" || in.Error != "" {
		t.Errorf("out=%+v in=%+v", out, in)
	}
}

func TestListReturnURLFromReferer(t *testing.T) {
	cases := []struct{ ref, want string }{
		{"http://admin.test/admin/products?cat=men&page=2&toast=x", "/admin/products?cat=men&page=2"},
		{"http://admin.test/admin/products", "/admin/products"},
		{"http://evil.test/admin/products?cat=men", ""},
		{"http://admin.test/admin/orders", ""},
		{"", ""},
		{"::bad", ""},
	}
	for _, c := range cases {
		if got := listReturnURL(c.ref, "admin.test", productsListPath); got != c.want {
			t.Errorf("listReturnURL(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}

func TestSaveProductReturnsToListPage(t *testing.T) {
	saver := &fakeSaver{}
	h, mock := newProductFormHandlers(t, saver)
	mock.ExpectQuery(`FROM points_of_sale`).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "city", "address", "working_hours", "latitude", "longitude", "is_active", "created_at"}))

	form := url.Values{
		"category_id": {"cat1"}, "name_ru": {"Nike"}, "name_ky": {"Nike"}, "base_price": {"4500"},
		"back": {"/admin/products?cat=men&page=2"},
	}
	w := postProductForm(h, form)

	if !saver.called {
		t.Fatalf("not saved: %d %.300s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/admin/products?cat=men&page=2&toast=") {
		t.Errorf("Location = %q, want the list page it came from", loc)
	}

	saver2 := &fakeSaver{}
	h2, mock2 := newProductFormHandlers(t, saver2)
	mock2.ExpectQuery(`FROM points_of_sale`).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "city", "address", "working_hours", "latitude", "longitude", "is_active", "created_at"}))
	form.Set("back", "https://evil.test/admin/products")
	w = postProductForm(h2, form)
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/products?toast=") {
		t.Errorf("open redirect: Location = %q", loc)
	}
}
