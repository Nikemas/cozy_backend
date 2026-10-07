package admin

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

var testPlaceholder = regexp.MustCompile(`\{[a-z_]+\}`)

func placeholderSet(s string) []string {
	out := testPlaceholder.FindAllString(s, -1)
	sort.Strings(out)
	return slices.Compact(out)
}

// TestAuditSummaryKeysTranslated: every summary key the journal writes is
// in both admin locales with the same placeholders.
func TestAuditSummaryKeysTranslated(t *testing.T) {
	for _, k := range audit.SummaryKeys {
		if !adminBundle.Has(i18n.LangRU, k) || !adminBundle.Has(i18n.LangKY, k) {
			t.Errorf("summary key %q missing in admin.ru.yaml or admin.ky.yaml", k)
			continue
		}
		ru, ky := placeholderSet(adminBundle.T(i18n.LangRU, k)), placeholderSet(adminBundle.T(i18n.LangKY, k))
		if !slices.Equal(ru, ky) {
			t.Errorf("summary key %q: placeholders ru %v, ky %v", k, ru, ky)
		}
	}
}

// entryRow is e as the journal page reads it back from the database.
func entryRow(t *testing.T, e audit.Entry) audit.Row {
	t.Helper()
	b, err := json.Marshal(e.StoredDetails())
	if err != nil {
		t.Fatal(err)
	}
	return detailsRow(t, e.Summary, string(b))
}

func detailsRow(t *testing.T, summary, detailsJSON string) audit.Row {
	t.Helper()
	row := audit.Row{Summary: summary}
	if err := json.Unmarshal([]byte(detailsJSON), &row.Details); err != nil {
		t.Fatalf("details %q: %v", detailsJSON, err)
	}
	return row
}

// checkTranslatedRow: the row carries a known message key, the Russian
// rendering reproduces the stored Summary (so every argument is there),
// and the Kyrgyz rendering fills every placeholder.
func checkTranslatedRow(t *testing.T, row audit.Row) {
	t.Helper()
	key, _ := row.Details[audit.DetailMsgKey].(string)
	if !slices.Contains(audit.SummaryKeys, key) {
		t.Errorf("summary %q: msg_key %q is not a known summary key", row.Summary, key)
		return
	}
	if got := localizedAuditSummary(ruTr, row); got != row.Summary {
		t.Errorf("ru rendering of %s = %q, want the stored %q", key, got, row.Summary)
	}
	args, _ := row.Details[audit.DetailMsgArgs].(map[string]any)
	if ky, ok := fillAuditSummary(kyTr, kyTr.T(key), args); !ok || strings.Contains(ky, "{") {
		t.Errorf("ky rendering of %s = %q (ok=%v)", key, ky, ok)
	}
}

// argCapture is a sqlmock argument that records every string it sees.
type argCapture struct{ got *[]string }

func (c argCapture) Match(v driver.Value) bool {
	s, ok := v.(string)
	if ok {
		*c.got = append(*c.got, s)
	}
	return ok
}

// TestAuditEntryBuildersCarryMessages covers every entry builder of the
// product form and the category screen.
func TestAuditEntryBuildersCarryMessages(t *testing.T) {
	brand := "Nike"
	old := &productSnapshot{CategoryID: "c1", NameRu: "Кеды", NameKy: "Кеды", Brand: &brand, BasePrice: 4500}
	upd := productSaveInput{
		ProductID: "p1",
		Product:   catalog.ProductInput{CategoryID: "c2", NameRu: "Кеды", NameKy: "Кеды", Brand: &brand, BasePrice: 4900},
		Variants: []variantRowInput{
			{Key: "v2", ID: "v2", Size: "43", Color: "Синий"},
			{Key: "n1", Size: "44", Color: "Белый"},
		},
	}
	oldVariants := map[string]variantSnapshot{"v2": {Size: "43", Color: "Чёрный"}, "v3": {Size: "45", Color: "Белый"}}
	create := productSaveInput{Product: catalog.ProductInput{CategoryID: "c1", NameRu: "Кеды", NameKy: "Кеды", BasePrice: 100}}

	var entries []audit.Entry
	entries = append(entries, productSaveEntries("p1", upd, old, oldVariants, map[string]string{"v2": "v2", "n1": "vNew"})...)
	entries = append(entries, productSaveEntries("p1", upd, nil, nil, map[string]string{"v2": "v2", "n1": "vNew"})...)
	entries = append(entries, productSaveEntries("pNew", create, nil, nil, nil)...)
	for _, action := range []string{audit.ActionCategoryCreate, audit.ActionCategoryUpdate, audit.ActionCategoryDelete} {
		entries = append(entries, categoryAuditEntry(action, "c1", "Кеды", nil))
	}
	entries = append(entries, categoryAuditEntry(audit.ActionCategoryDelete, "c1", "", nil))

	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.MsgKey] = true
		checkTranslatedRow(t, entryRow(t, e))
	}
	for _, want := range []string{
		audit.MsgProductCreated, audit.MsgProductUpdated, audit.MsgProductUpdatedFields,
		audit.MsgVariantChanged, audit.MsgVariantAdded, audit.MsgVariantRemoved,
		audit.MsgCategoryCreated, audit.MsgCategoryUpdated, audit.MsgCategoryDeleted, audit.MsgCategoryDeletedNoName,
	} {
		if !seen[want] {
			t.Errorf("no builder produced %s", want)
		}
	}
}

func TestStockEntryCarriesMessage(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM product_variants v JOIN products p`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "pid", "name", "size", "color"}).AddRow("v1", "p1", "Nike", "42", "Белый"))
	mock.ExpectQuery(`FROM points_of_sale`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow("pA", "Дордой"))
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := stockEntriesTx(context.Background(), tx, map[string]string{"v1": "v1"},
		[]stockCellChange{{RowKey: "v1", PointID: "pA", Qty: 4, Orig: intPtr(2)}})
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	if entries[0].MsgKey != audit.MsgStockChanged {
		t.Errorf("msg_key = %q", entries[0].MsgKey)
	}
	row := entryRow(t, entries[0])
	checkTranslatedRow(t, row)
	if got := localizedAuditSummary(kyTr, row); got != "«Nike» 42 / Белый калдыгы, Дордой: 2 → 4" {
		t.Errorf("ky stock summary = %q", got)
	}
}

// TestRecordedAuditEntriesCarryMessages covers the actions journaled
// straight from handlers: product row actions, points and staff.
func TestRecordedAuditEntriesCarryMessages(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var summaries, details []string
	h := &handlers{audit: audit.New(db)}
	ctx := ownerCtx()
	in := points.PointInput{Name: "Дордой", Address: "ул. 1", IsActive: true}

	calls := []func(){
		func() { h.auditProductActive(ctx, "p1", "Кеды", true) },
		func() { h.auditProductActive(ctx, "p1", "Кеды", false) },
		func() { h.auditProductDeleted(ctx, "p1") },
		func() { h.auditCategory(ctx, audit.ActionCategoryCreate, "c1", "Кеды", nil) },
		func() { h.auditPoint(ctx, audit.ActionPointCreate, "pt1", nil, in) },
		func() { h.auditPoint(ctx, audit.ActionPointActivate, "pt1", nil, in) },
		func() {
			off := in
			off.IsActive = false
			h.auditPoint(ctx, audit.ActionPointDeactivate, "pt1", nil, off)
		},
		func() { h.auditPoint(ctx, audit.ActionPointUpdate, "pt1", &points.Point{Name: "Старое"}, in) },
		func() { h.auditStaff(ctx, audit.ActionStaffCreate, "st1", "Нурлан", nil) },
		func() { h.auditStaff(ctx, audit.ActionStaffActivate, "st1", "Нурлан", nil) },
		func() { h.auditStaff(ctx, audit.ActionStaffDeactivate, "st1", "Нурлан", nil) },
		func() { h.auditStaff(ctx, audit.ActionStaffPassword, "st1", "Нурлан", nil) },
	}
	for range calls {
		mock.ExpectExec(`INSERT INTO audit_log`).
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
				argCapture{&summaries}, argCapture{&details}, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	for _, call := range calls {
		call()
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if len(summaries) != len(calls) || len(details) != len(calls) {
		t.Fatalf("captured %d summaries / %d details, want %d", len(summaries), len(details), len(calls))
	}
	seen := map[string]bool{}
	for i := range calls {
		row := detailsRow(t, summaries[i], details[i])
		key, _ := row.Details[audit.DetailMsgKey].(string)
		seen[key] = true
		checkTranslatedRow(t, row)
	}
	for _, want := range []string{
		audit.MsgProductActivated, audit.MsgProductDeactivated, audit.MsgProductDeleted, audit.MsgCategoryCreated,
		audit.MsgPointCreated, audit.MsgPointEnabled, audit.MsgPointDisabled, audit.MsgPointUpdated,
		audit.MsgStaffCreated, audit.MsgStaffEnabled, audit.MsgStaffDisabled, audit.MsgStaffPasswordReset,
	} {
		if !seen[want] {
			t.Errorf("no recorded entry carried %s", want)
		}
	}
}

func TestLocalizedAuditSummary(t *testing.T) {
	cases := []struct {
		name    string
		tr      tr
		details string
		summary string
		want    string
	}{
		{"ky translation", kyTr, `{"msg_key":"admin.audit.summary.product_activated","msg_args":{"name":"Кеды"}}`,
			"Товар «Кеды» активирован", "«Кеды» товары активдештирилди"},
		{"ky bulk suffix", kyTr, `{"msg_key":"admin.audit.summary.product_category","msg_args":{"name":"Кеды","from":"А","to":"Б"},"msg_via":"bulk"}`,
			"Товар «Кеды»: категория «А» → «Б» (массово)", "«Кеды» товары: категория «А» → «Б» (массалык түрдө)"},
		{"ky field list", kyTr, `{"msg_key":"admin.audit.summary.product_updated_fields","msg_args":{"name":"Кеды","fields":["base_price","brand"]}}`,
			"Изменён товар «Кеды»: бренд, цена", "«Кеды» товары өзгөртүлдү: баасы, бренд"},
		{"ky api count", kyTr, `{"msg_key":"admin.audit.summary.product_images","msg_args":{"count":3},"msg_via":"api"}`,
			"Фото товара заменены: 3 шт. (API)", "Товардын сүрөттөрү алмаштырылды: 3 даана (API)"},
		{"old row without msg_key", kyTr, `{"base_price":{"from":1,"to":2}}`, "Изменён товар «Кеды»: цена", "Изменён товар «Кеды»: цена"},
		{"unknown key", kyTr, `{"msg_key":"admin.audit.summary.nope"}`, "старая сводка", "старая сводка"},
		{"missing argument", kyTr, `{"msg_key":"admin.audit.summary.product_activated"}`, "старая сводка", "старая сводка"},
		{"unknown source", kyTr, `{"msg_key":"admin.audit.summary.product_deleted","msg_via":"cron"}`, "старая сводка", "старая сводка"},
		{"placeholder-like name stays as typed", kyTr, `{"msg_key":"admin.audit.summary.staff_created","msg_args":{"name":"{name}"}}`,
			"x", "«{name}» кызматкери кошулду"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := localizedAuditSummary(c.tr, detailsRow(t, c.summary, c.details)); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestAuditPageKyShowsTranslatedSummary: the Kyrgyz journal translates
// rows that carry a message, keeps the stored Summary for old rows,
// escapes staff-typed names and hides msg_* from the details column.
func TestAuditPageKyShowsTranslatedSummary(t *testing.T) {
	newRow := detailsRow(t, "Создан товар «<script>alert(1)</script>»",
		`{"msg_key":"admin.audit.summary.product_created","msg_args":{"name":"<script>alert(1)</script>"},"brand":"Nike"}`)
	newRow.ID, newRow.At, newRow.Action, newRow.EntityType, newRow.EntityID = "a1", time.Now(), audit.ActionProductCreate, audit.EntityProduct, uuid1
	oldRow := audit.Row{ID: "a2", At: time.Now(), Action: audit.ActionProductUpdate, EntityType: audit.EntityProduct, EntityID: uuid1,
		Summary: "Изменён товар «Кеды»: цена", Details: map[string]any{"base_price": map[string]any{"from": 4000.0, "to": 4500.0}}}
	lister := &fakeAuditLister{total: 2, rows: []audit.Row{newRow, oldRow}}
	h := &handlers{render: newTestRenderer(t), auditList: lister}
	r := requestAs(http.MethodGet, "/admin/audit", &staff.Staff{ID: "s1", Name: "Айгерим", Role: staff.RoleOwner}, nil)
	r.AddCookie(&http.Cookie{Name: langCookieName, Value: "ky"})
	w := httptest.NewRecorder()
	h.auditPage(withLang(w, r), r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		"«&lt;script&gt;alert(1)&lt;/script&gt;» товары түзүлдү",
		"Изменён товар «Кеды»: цена",
		"бренд: Nike",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	for _, unwanted := range []string{"<script>alert(1)", "msg_key", "msg_args", "admin.audit.summary"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("body contains %q", unwanted)
		}
	}
}

func TestRussianAuditSummaryFromLocale(t *testing.T) {
	// Arrange
	args := audit.Args{"name": "Кеды"}

	// Act
	got := russianAuditSummary(audit.MsgProductActivated, args, audit.ViaBulk)

	// Assert: the stored Summary is exactly the Russian locale line.
	if want := "Товар «Кеды» активирован (массово)"; got != want {
		t.Errorf("russianAuditSummary = %q, want %q", got, want)
	}
	if got := russianAuditSummary(audit.MsgProductActivated, args, ""); got != "Товар «Кеды» активирован" {
		t.Errorf("without via = %q", got)
	}
}
