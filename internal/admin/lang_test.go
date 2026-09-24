package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

var kyTr = trFor(i18n.LangKY)

// TestAdminLocaleKeySetsMatch: admin.ru.yaml and admin.ky.yaml define
// exactly the same keys, every value is non-empty, and a format value has
// the same number of %-verbs in both languages.
func TestAdminLocaleKeySetsMatch(t *testing.T) {
	ru, ky := adminBundle.Keys(i18n.LangRU), adminBundle.Keys(i18n.LangKY)
	if len(ru) == 0 {
		t.Fatal("admin.ru.yaml has no keys")
	}
	inKY := map[string]bool{}
	for _, k := range ky {
		inKY[k] = true
	}
	inRU := map[string]bool{}
	for _, k := range ru {
		inRU[k] = true
		if !inKY[k] {
			t.Errorf("key %q is in admin.ru.yaml but not in admin.ky.yaml", k)
		}
	}
	for _, k := range ky {
		if !inRU[k] {
			t.Errorf("key %q is in admin.ky.yaml but not in admin.ru.yaml", k)
		}
	}
	verbs := regexp.MustCompile(`%[sdv]`)
	for _, k := range ru {
		if !strings.HasPrefix(k, "admin.") {
			t.Errorf("key %q must start with admin.", k)
		}
		rv, kv := adminBundle.T(i18n.LangRU, k), adminBundle.T(i18n.LangKY, k)
		if strings.TrimSpace(rv) == "" || strings.TrimSpace(kv) == "" {
			t.Errorf("key %q has an empty value", k)
		}
		if a, b := len(verbs.FindAllString(rv, -1)), len(verbs.FindAllString(kv, -1)); a != b {
			t.Errorf("key %q: %d format verbs in ru, %d in ky", k, a, b)
		}
	}
}

// adminKeyLiteral matches locale keys written literally in templates
// ({{t "admin.x"}}, {{tf "admin.x" ...}}) and Go (t.T("admin.x"), key
// tables such as navDefs).
var adminKeyLiteral = regexp.MustCompile(`"(admin\.[a-z0-9_]+(?:\.[a-z0-9_]+)+)"`)

// TestAdminKeysUsedExist scans every admin template and non-test Go file
// of this package for literal "admin.*" keys and checks each exists in
// the locale files (a plural base "admin.plural.x" must have .one/.few/
// .many). A typo'd key would otherwise render as the raw key.
func TestAdminKeysUsedExist(t *testing.T) {
	root := repoRoot(t)
	files, err := filepath.Glob(filepath.Join(root, templatesDir, "*.gohtml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no admin templates found (err=%v)", err)
	}
	goFiles, _ := filepath.Glob(filepath.Join(root, "internal", "admin", "*.go"))
	for _, f := range goFiles {
		if !strings.HasSuffix(f, "_test.go") {
			files = append(files, f)
		}
	}

	seen := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range adminKeyLiteral.FindAllStringSubmatch(string(b), -1) {
			key := m[1]
			seen++
			if adminBundle.Has(i18n.LangRU, key) {
				continue
			}
			if adminBundle.Has(i18n.LangRU, key+".one") && adminBundle.Has(i18n.LangRU, key+".few") && adminBundle.Has(i18n.LangRU, key+".many") {
				continue
			}
			t.Errorf("%s: key %q is not in locales/admin.*.yaml", filepath.Base(f), key)
		}
	}
	if seen < 100 {
		t.Errorf("only %d admin.* key usages found — is the scan pattern still right?", seen)
	}
	for _, role := range []staff.Role{staff.RoleOwner, staff.RoleManager, staff.RolePointStaff} {
		if k := roleLabel(role); !adminBundle.Has(i18n.LangKY, k) {
			t.Errorf("roleLabel(%s) = %q, not a locale key", role, k)
		}
	}
}

func TestTrPluralAndFormat(t *testing.T) {
	cases := []struct {
		tr   tr
		n    int
		want string
	}{
		{ruTr, 1, "1 заказ"}, {ruTr, 3, "3 заказа"}, {ruTr, 11, "11 заказов"},
		{kyTr, 1, "1 буйрутма"}, {kyTr, 5, "5 буйрутма"},
	}
	for _, c := range cases {
		if got := c.tr.N(c.n, "admin.plural.order"); got != c.want {
			t.Errorf("%s N(%d) = %q, want %q", c.tr.Lang(), c.n, got, c.want)
		}
	}
	if got := kyTr.F("admin.order.title", "COZY-1"); got != "COZY-1 буйрутмасы" {
		t.Errorf("ky order title = %q", got)
	}
	if got := kyTr.T("not.a.key"); got != "not.a.key" {
		t.Errorf("unknown key should come back as is, got %q", got)
	}
	if got := (tr{}).T("admin.nav.orders"); got != "Заказы" {
		t.Errorf("zero tr should be Russian, got %q", got)
	}
}

func TestAppErrMessageLocalized(t *testing.T) {
	err := apperr.Conflict("last_owner", "нельзя понизить или деактивировать последнего владельца")
	if got := appErrMessage(ruTr, err); got != err.Message {
		t.Errorf("ru = %q, want the service message", got)
	}
	if got := appErrMessage(kyTr, err); got != kyTr.T("admin.apperr.last_owner") {
		t.Errorf("ky = %q", got)
	}
	unknown := apperr.BadRequest("some_new_code", "что-то новое")
	if got := appErrMessage(kyTr, unknown); got != "что-то новое" {
		t.Errorf("ky unknown code = %q, want the Russian fallback", got)
	}
	if got := appErrMessage(kyTr, context.Canceled); got != kyTr.T("admin.err.generic") {
		t.Errorf("ky generic = %q", got)
	}
	if got := errText(kyTr, localizedError{"admin.qty.err_negative"}); got != "саны терс болбошу керек" {
		t.Errorf("errText ky = %q", got)
	}
	if got := (localizedError{"admin.qty.err_negative"}).Error(); got != "количество не может быть отрицательным" {
		t.Errorf("localizedError.Error = %q", got)
	}
}

func TestSetLangCookieAndRedirect(t *testing.T) {
	h := &handlers{}
	cases := []struct {
		form     url.Values
		referer  string
		wantLang string
		wantLoc  string
	}{
		{url.Values{"lang": {"ky"}, "next": {"/admin/orders?status=placed"}}, "", "ky", "/admin/orders?status=placed"},
		{url.Values{"lang": {"ru"}}, "https://cozy.kg/admin/stock?point=1", "ru", "/admin/stock?point=1"},
		{url.Values{"lang": {"xx"}, "next": {"https://evil.example/admin"}}, "", "ru", "/admin/login"},
		{url.Values{"lang": {"ky"}, "next": {"//evil.example/admin/"}}, "https://evil.example/", "ky", "/admin/login"},
		{url.Values{"lang": {"ky"}, "next": {"/adminx"}}, "", "ky", "/admin/login"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/admin/lang", strings.NewReader(c.form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if c.referer != "" {
			req.Header.Set("Referer", c.referer)
		}
		w := httptest.NewRecorder()
		h.setLang(w, req)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("status = %d", w.Code)
		}
		if loc := w.Header().Get("Location"); loc != c.wantLoc {
			t.Errorf("form %v: Location = %q, want %q", c.form, loc, c.wantLoc)
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != langCookieName || cookies[0].Value != c.wantLang || cookies[0].Path != "/admin" || !cookies[0].HttpOnly {
			t.Errorf("form %v: cookie = %+v", c.form, cookies)
		}
	}
}

// TestAuthGateCarriesLanguage: behind requireStaffRole, both the request
// context and the ResponseWriter carry the admin_lang cookie's language,
// so Render and view builders need no extra plumbing.
func TestAuthGateCarriesLanguage(t *testing.T) {
	resolver := &fakeResolver{st: &staff.Staff{ID: "s1", Name: "A B", Role: staff.RoleOwner, IsActive: true}}
	var ctxLang, writerLang, reqLang string
	handler := requireStaffRole(resolver, staff.RoleOwner)(func(w http.ResponseWriter, r *http.Request) {
		ctxLang = trFromContext(r.Context()).Lang()
		writerLang = langFromWriter(w)
		reqLang = langFromRequest(r)
	})
	req := httptest.NewRequest(http.MethodGet, "/admin/orders", nil)
	req.AddCookie(&http.Cookie{Name: langCookieName, Value: "ky"})
	handler(httptest.NewRecorder(), req)
	if ctxLang != "ky" || writerLang != "ky" || reqLang != "ky" {
		t.Errorf("ctx=%q writer=%q request=%q, want ky everywhere", ctxLang, writerLang, reqLang)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/orders", nil)
	handler(httptest.NewRecorder(), req)
	if ctxLang != "ru" || writerLang != "ru" {
		t.Errorf("no cookie: ctx=%q writer=%q, want ru", ctxLang, writerLang)
	}
}

// kyScreenFixtures builds every admin screen's PageData with populated
// data (labels produced by the Kyrgyz builders where they exist), so
// most template branches execute.
func kyScreenFixtures() map[string]PageData {
	owner := &staff.Staff{ID: "s1", Name: "Aigerim B", Role: staff.RoleOwner, IsActive: true}
	pointID := "pt1"
	pointStaff := staff.Staff{ID: "s3", Name: "Daniyar K", Phone: "+996555000001", Role: staff.RolePointStaff, PointID: &pointID, IsActive: true}
	shell := func(screen, title string) PageData {
		return PageData{
			Lang: i18n.LangKY, Screen: screen, PageTitle: title, ShowSidebar: true, ShowBack: true, ShowSearch: true,
			Staff: owner, Initials: initialsFor(owner.Name), RoleLabel: roleLabel(owner.Role),
			NavItems: navItemsForRole(owner.Role, screen), Toast: "ok",
		}
	}
	stockPoints := []StockPointVM{{ID: "pt1", Name: "Main"}, {ID: "pt2", Name: "Dordoi", Inactive: true}}
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	from := orders.StatusPlaced
	paid := orders.PaymentPaid

	out := map[string]PageData{}

	login := PageData{Lang: i18n.LangKY, Screen: "login", PageTitle: "admin.login.title", Err: "x"}
	out["login"] = login
	out["no_access"] = PageData{Lang: i18n.LangKY, Screen: "no_access", PageTitle: "admin.no_access.title", Staff: owner}

	ordersPage := shell("orders", "admin.nav.orders")
	chips := []StatusChipLink{}
	for _, f := range orderStatusFilters {
		chips = append(chips, StatusChipLink{Label: kyTr.T(f.Label), URL: "/admin/orders", Class: f.Class})
	}
	meta := orderStatusMetaFor(kyTr, orders.StatusPlaced)
	ordersPage.Data = OrdersListData{
		StatusChips:  chips,
		RangeOptions: []RangeOptionLink{{Value: "custom", Label: kyTr.T("admin.range.custom"), Selected: true}},
		Rows: []OrderRowView{{URL: "/admin/orders/o1", Number: "COZY-1", DateLabel: "01.01.2026", ItemsCount: 2,
			ItemsLabel: kyTr.N(2, "admin.plural.product"), TotalLabel: "7 900 сом",
			PaymentLabel: paymentLabel(kyTr, orders.PaymentOnlineCard, &paid), StatusLabel: meta.Label, StatusClass: meta.Class}},
		CanChoosePoint: true, Points: []PointOptionVM{{ID: "", Name: kyTr.T("admin.common.all_points")}},
		Notes: []string{kyTr.T("admin.orders.note_bad_status")}, Filtered: true, Range: "custom",
		CountLabel: kyTr.N(1, "admin.plural.order"), HasPrev: true, HasNext: true,
	}
	out["orders"] = ordersPage

	detail := shell("orders", kyTr.F("admin.order.title", "COZY-1"))
	detail.Data = OrderDetailData{
		ID: "o1", Number: "COZY-1", StatusLabel: meta.Label, StatusClass: meta.Class,
		PaymentLabel: paymentLabel(kyTr, orders.PaymentCashOnDelivery, nil),
		AddressText:  kyTr.F("admin.order.pickup_at", "Main", "Street 1"), Comment: "—",
		Items:         []OrderDetailItemView{{Name: "Nike", Variant: kyTr.F("admin.order.item_variant", "42", "white"), Qty: 1, PriceLabel: "6 500 сом"}},
		TotalLabel:    "6 500 сом",
		StatusButtons: buildStatusButtons(kyTr, orders.StatusPlaced, staff.RoleOwner),
		OrderHistoryData: buildOrderHistoryData(kyTr, &orders.Order{DeliveryFee: 200, RefundRequired: true, History: []orders.StatusChange{
			{ToStatus: orders.StatusPlaced, ActorType: orders.ActorCustomer, CreatedAt: now},
			{FromStatus: &from, ToStatus: orders.StatusCancelled, ActorType: orders.ActorSystem, CreatedAt: now},
		}}),
	}
	out["order_detail"] = detail

	products := shell("products", "admin.nav.products")
	stockLbl, fg, bg := stockChip(kyTr, 3)
	products.Data = ProductsPageData{
		CanEdit: true, CanDelete: true, ShowSubs: true,
		CategoryChips: []ChipVM{{Label: kyTr.T("admin.common.all"), URL: "/admin/products", Active: true}},
		SubChips:      []ChipVM{{Label: kyTr.T("admin.common.all"), URL: "/admin/products"}},
		Products: []ProductRowVM{{ID: "p1", Name: "Shoe", PriceText: "1 000 сом", VariantsLabel: variantsLabel(kyTr, 2),
			StockLabel: stockLbl, StockFG: fg, StockBG: bg, StatusLabel: statusLabel(kyTr, true), EditURL: "/admin/products/p1",
			DeactivateLabel: deactivateLabel(kyTr, true), DeleteURL: "/admin/products/p1/delete"}},
		CountLabel: countLabel(kyTr, 1), PageSize: 25, NewURL: "/admin/products/new", ImportURL: "/admin/products/import",
	}
	out["products"] = products

	form := shell("product_form", "admin.product.edit_title")
	cats := []CategoryOptionVM{{ID: "c1", Name: "Men", Slug: "men"}}
	form.Data = ProductFormData{
		IsEdit: true, ProductID: "p1", NameRu: "Shoe", NameKy: "Shoe", Categories: cats, CategoriesJSON: categoryOptionsJSON(cats),
		Variants: buildVariantRows(kyTr, []catalog.Variant{{ID: "v1", Size: "42", Color: "white"}},
			[]catalog.StockEntry{{VariantID: "v1", PointID: "pt1", Quantity: 3}}, stockPoints),
		StockPoints: stockPoints, StockPointsJSON: marshalJS(stockPoints), CanDelete: true, Err: "x",
	}
	out["product_form"] = form

	importPage := shell("product_import", "admin.import.title")
	importPage.Data = ImportPageData{ImportURL: "/admin/products/import"}
	out["product_import"] = importPage

	reportsPage := shell("reports", "admin.nav.reports")
	reportsPage.Data = ReportsData{
		Periods: reportPeriodOptions(kyTr, "custom"), Stats: buildStatCards(kyTr, nil),
		Bars:        buildBars(nil, now, now),
		TopProducts: buildTopProducts(kyTr, nil), Categories: buildCategoryBars(kyTr, nil),
		Custom: true, Err: "x",
	}
	out["reports"] = reportsPage

	pointsPage := shell("points", "admin.nav.points")
	pointsPage.Data = pointsPageData{Rows: []pointRow{
		newPointRow(kyTr, &points.Point{ID: "pt1", Name: "Main", Address: "Street 1", IsActive: true}),
		newPointRow(kyTr, &points.Point{ID: "pt2", Name: "Dordoi", Address: "Street 2"}),
	}, Error: "x"}
	out["points"] = pointsPage

	staffPage := shell("staff", "admin.nav.staff")
	staffPage.Data = staffPageData{
		Rows:   []staffRow{newStaffRow(kyTr, *owner, nil), newStaffRow(kyTr, pointStaff, map[string]string{"pt1": "Main"})},
		Points: []*points.Point{{ID: "pt1", Name: "Main"}}, Error: "x", Notice: "y", MinPasswordLength: 8,
	}
	out["staff"] = staffPage

	categories := shell("categories", "admin.nav.categories")
	categories.Data = categoriesPageData{
		Rows:    []categoryRow{{ID: "c1", NameRu: "Men", NameKy: "Men", Slug: "men"}},
		Parents: []categoryParentOption{{ID: "c1", Name: "Men"}}, Error: "x",
	}
	out["categories"] = categories

	stock := shell("stock", "admin.nav.stock")
	stock.Data = StockPageData{
		CanChoosePoint: true, Points: []PointOptionVM{{ID: "pt1", Name: "Main", Selected: true}}, PointID: "pt1", PointName: "Main",
		Rows:       []StockPageRowVM{buildStockPageRow(kyTr, stockPageRow{VariantID: "v1", ProductID: "p1", ProductName: "Shoe", Size: "42", Color: "white", Quantity: 0}, "pt1", "Main", true, nil)},
		CountLabel: kyTr.N(1, "admin.plural.variant"), HasPrev: true, HasNext: true, Err: "x",
	}
	out["stock"] = stock
	return out
}

// TestRenderEveryScreenInKyrgyz renders every admin screen in Kyrgyz and
// checks: <html lang="ky">, no raw "admin.*" key leaked, and none of the
// Russian UI strings (whose Kyrgyz translation differs) remains — i.e.
// the template text is really localized, not just the chrome.
func TestRenderEveryScreenInKyrgyz(t *testing.T) {
	rr := newTestRenderer(t)
	fixtures := kyScreenFixtures()
	for screen := range screenPages {
		if _, ok := fixtures[screen]; !ok {
			t.Errorf("no Kyrgyz fixture for screen %q — add one to kyScreenFixtures", screen)
		}
	}

	rawKey := regexp.MustCompile(`admin\.[a-z_]+\.[a-z_.]+`)
	// Russian strings that must not appear in a Kyrgyz page: every ru
	// value that differs from its ky translation, long enough not to
	// collide with an unrelated word, without format verbs.
	var russian []string
	for _, k := range adminBundle.Keys(i18n.LangRU) {
		rv, kv := adminBundle.T(i18n.LangRU, k), adminBundle.T(i18n.LangKY, k)
		if rv == kv || len([]rune(rv)) < 5 || strings.Contains(rv, "%") || !hasCyrillic(rv) || strings.HasPrefix(k, "admin.apperr.") {
			continue
		}
		russian = append(russian, rv)
	}

	for screen, data := range fixtures {
		t.Run(screen, func(t *testing.T) {
			w := httptest.NewRecorder()
			if err := rr.Render(w, screen, data); err != nil {
				t.Fatalf("Render: %v", err)
			}
			body := w.Body.String()
			if !strings.Contains(body, `<html lang="ky">`) {
				t.Error(`missing <html lang="ky">`)
			}
			if m := rawKey.FindString(body); m != "" {
				t.Errorf("raw locale key %q rendered", m)
			}
			text := visibleText(body)
			for _, rv := range russian {
				if strings.Contains(text, rv) {
					t.Errorf("Russian UI string %q left in the Kyrgyz page", rv)
				}
			}
		})
	}
}

// TestRenderPicksLanguageFromWriter: a page whose PageData has no Lang
// renders in the language the auth-gate attached to the writer.
func TestRenderPicksLanguageFromWriter(t *testing.T) {
	rr := newTestRenderer(t)
	data := kyScreenFixtures()["points"]
	data.Lang = ""
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/points", nil)
	req.AddCookie(&http.Cookie{Name: langCookieName, Value: "ky"})
	if err := rr.Render(withLang(rec, req), "points", data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Body.String(), "Кызматкерлер") || !strings.Contains(rec.Body.String(), `lang="ky"`) {
		t.Error("expected the Kyrgyz page")
	}

	rec = httptest.NewRecorder()
	if err := rr.Render(rec, "points", data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Body.String(), "Сотрудники") || !strings.Contains(rec.Body.String(), `<html lang="ru">`) {
		t.Error("expected the Russian page by default")
	}
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// visibleText drops HTML comments and the CYR_TO_LAT transliteration
// table (Cyrillic by design), keeping everything else — including
// attribute values and script strings, which staff also see.
func visibleText(body string) string {
	if i := strings.Index(body, "var CYR_TO_LAT"); i >= 0 {
		if j := strings.Index(body[i:], "};"); j >= 0 {
			body = body[:i] + body[i+j:]
		}
	}
	return body
}
