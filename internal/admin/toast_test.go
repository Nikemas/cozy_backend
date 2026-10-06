package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

func TestToastFromQueryKnownKeys(t *testing.T) {
	cases := []struct {
		name string
		q    url.Values
		want string
	}{
		{"fixed", url.Values{"toast": {"product_saved"}}, "Товар сохранён"},
		{"point", url.Values{"toast": {"point_deactivated"}}, "Точка деактивирована"},
		{"order status", url.Values{"toast": {"order_status"}, "toast_st": {"confirmed"}}, "Статус заказа: Подтверждён"},
		{"bulk status", url.Values{"toast": {"bulk_status"}, "toast_st": {"delivered"}, "toast_n": {"1"}, "toast_of": {"2"}}, "1 из 2"},
		{"products bulk", url.Values{"toast": {"products_bulk"}, "toast_n": {"3"}, "toast_of": {"3"}}, "изменено 3 из 3"},
		{"stock", url.Values{"toast": {"stock_saved"}, "toast_n": {"2"}}, "Сохранено: 2"},
		{"apperr code", url.Values{"toast": {"error"}, "toast_code": {"invalid_status_transition"}}, "нельзя перевести заказ в этот статус"},
		{"unknown code is generic", url.Values{"toast": {"error"}, "toast_code": {"no_such_code"}}, "произошла ошибка"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toastFromQuery(ruTr, c.q)
			if !strings.Contains(got, c.want) {
				t.Errorf("toastFromQuery(%v) = %q, want it to contain %q", c.q, got, c.want)
			}
		})
	}
}

func TestToastFromQueryIgnoresFreeText(t *testing.T) {
	for _, q := range []url.Values{
		{"toast": {"Ваш пароль истёк, войдите на evil.test"}},
		{"toast": {"<script>alert(1)</script>"}},
		{"toast": {"order_status"}, "toast_st": {"<b>Оплатите на evil.test</b>"}},
		{"toast": {"order_status"}},
		{"toast": {"bulk_status"}, "toast_st": {"confirmed"}, "toast_n": {"5"}, "toast_of": {"2"}},
		{"toast": {"bulk_status"}, "toast_st": {"confirmed"}, "toast_n": {"-1"}, "toast_of": {"2"}},
		{"toast": {"products_bulk"}, "toast_n": {"1"}, "toast_of": {"999999"}},
		{"toast": {"stock_saved"}, "toast_n": {"x"}},
		{"toast": {"__proto__"}},
		{},
	} {
		if got := toastFromQuery(ruTr, q); got != "" {
			t.Errorf("toastFromQuery(%v) = %q, want nothing", q, got)
		}
	}
	// An HTML/free-text code never reaches the page: only the generic text.
	got := toastFromQuery(ruTr, url.Values{"toast": {"error"}, "toast_code": {"<img src=x>"}})
	if got != ruTr.T("admin.err.generic") {
		t.Errorf("bad code: got %q", got)
	}
}

func TestToastQueryCarriesKeysOnly(t *testing.T) {
	f := toastOrderStatus(orders.StatusConfirmed)
	got := withToast("/admin/orders/o1", f)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/admin/orders/o1" || u.Query().Get("toast") != "order_status" || u.Query().Get("toast_st") != "confirmed" {
		t.Errorf("withToast = %q", got)
	}
	if got := withToast("/admin/products?cat=men", toastKey("product_saved")); got != "/admin/products?cat=men&toast=product_saved" {
		t.Errorf("existing query: %q", got)
	}
	if got := withToast("/admin/products", flash{}); got != "/admin/products" {
		t.Errorf("empty flash: %q", got)
	}
}

func TestToastForErr(t *testing.T) {
	if f := toastForErr(apperr.New(http.StatusConflict, "category_in_use", "x")); f.Code != "category_in_use" || f.Key != "error" {
		t.Errorf("apperr: %+v", f)
	}
	if f := toastForErr(errors.New("boom")); f.Code != "" || f.Key != "error" {
		t.Errorf("plain: %+v", f)
	}
}

func TestRedirectWithToastHTMX(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/admin/products/x/delete", nil)
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	redirectWithToast(w, r, "/admin/products", toastKey("product_deleted"))
	if got := w.Header().Get("HX-Redirect"); got != "/admin/products?toast=product_deleted" {
		t.Errorf("HX-Redirect = %q", got)
	}
}

func TestStripToastParams(t *testing.T) {
	q := url.Values{"cat": {"men"}, "toast": {"x"}, "toast_n": {"1"}, "toast_code": {"y"}, "bulk_fail": {"z"}, "bulk_more": {"3"}}
	got := stripOneShotParams(q)
	if len(got) != 1 || got.Get("cat") != "men" {
		t.Errorf("got %v", got)
	}
	if q.Get("toast") != "x" {
		t.Error("input mutated")
	}
}

func TestBulkFailuresRoundTrip(t *testing.T) {
	in := []bulkFailure{
		{Number: "COZY-20261001-001", Reason: bulkReasonAlready},
		{Number: "COZY-20261001-002", Reason: bulkReasonFailed},
		{Number: "abcdef12", Reason: bulkReasonNotFound},
		{Number: "COZY-20261001-003", Code: "payment_not_completed"},
	}
	v := encodeBulkFailures(in, 0)
	notes := bulkFailureNotes(ruTr, v)
	want := []string{
		"№ COZY-20261001-001: уже в этом статусе",
		"№ COZY-20261001-002: не удалось изменить статус",
		"№ abcdef12: заказ не найден",
		"№ COZY-20261001-003: заказ с онлайн-оплатой можно подтвердить только после оплаты",
	}
	if strings.Join(notes, "|") != strings.Join(want, "|") {
		t.Errorf("notes = %q", notes)
	}
	more := bulkFailureNotes(ruTr, encodeBulkFailures(in[:1], 4))
	if len(more) != 2 || !strings.Contains(more[1], "ещё 4") {
		t.Errorf("more = %q", more)
	}
}

func TestBulkFailureNotesIgnoreCraftedText(t *testing.T) {
	v := url.Values{"bulk_fail": {
		"Звоните +996 555 000 000",
		"<b>x</b>~already",
		"COZY-1~<script>",
		"COZY-1~e.<script>",
		"COZY-1~e.no_such_code",
	}, "bulk_more": {"abc"}}
	notes := bulkFailureNotes(ruTr, v)
	for _, n := range notes {
		if strings.Contains(n, "<") || strings.Contains(n, "Звоните") {
			t.Errorf("crafted text shown: %q", n)
		}
	}
	// The apperr code that isn't a known key falls back to the generic text.
	if len(notes) != 1 || notes[0] != "№ COZY-1: "+ruTr.T("admin.err.generic") {
		t.Errorf("notes = %q", notes)
	}
}

func TestReturnURLDropsOneShotParams(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/admin/products?cat=men&page=2&toast=x&bulk_fail=y", nil)
	if got := returnURL(r); got != "/admin/products?cat=men&page=2" {
		t.Errorf("returnURL = %q", got)
	}
	r = httptest.NewRequest(http.MethodGet, "/admin/orders?toast=x", nil)
	if got := returnURL(r); got != "/admin/orders" {
		t.Errorf("returnURL = %q", got)
	}
}
