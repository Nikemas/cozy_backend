package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

func assertVaryLanguage(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	got := rec.Header().Values("Vary")
	n := 0
	for _, v := range got {
		if strings.EqualFold(v, "Accept-Language") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("Vary = %q, want exactly one Accept-Language", got)
	}
}

func TestAPILangFromAcceptLanguage(t *testing.T) {
	tests := []struct{ header, want string }{
		{"", i18n.LangRU},
		{"ky", i18n.LangKY},
		{"ky-KG,ru;q=0.8", i18n.LangKY},
		{"en", i18n.LangRU},
		{"ru", i18n.LangRU},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Language", tt.header)
		if got := apiLang(r); got != tt.want {
			t.Errorf("apiLang(%q) = %q, want %q", tt.header, got, tt.want)
		}
	}
}

func TestProductFacetsColorOptionsLocalized(t *testing.T) {
	tests := []struct {
		lang string
		want []colorOption
	}{
		{"ky", []colorOption{{Value: "Терракотовый", Label: "Терракотовый"}, {Value: "Черный", Label: "Кара"}}},
		{"ru", []colorOption{{Value: "Терракотовый", Label: "Терракотовый"}, {Value: "Черный", Label: "Черный"}}},
	}
	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			mux, mock := newFacetsMux(t)
			mock.ExpectQuery(`SELECT pv.size, pv.color,`).WillReturnRows(
				sqlmock.NewRows([]string{"size", "color", "min", "max"}).
					AddRow("40", "Черный", 100.0, 100.0).AddRow("40", "Терракотовый", 100.0, 100.0))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/products/facets", nil)
			req.Header.Set("Accept-Language", tt.lang)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			var body struct {
				Colors       []string      `json:"colors"`
				ColorOptions []colorOption `json:"color_options"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if strings.Join(body.Colors, ",") != "Терракотовый,Черный" {
				t.Errorf("colors = %v, want raw values unchanged", body.Colors)
			}
			if len(body.ColorOptions) != len(tt.want) {
				t.Fatalf("color_options = %+v, want %+v", body.ColorOptions, tt.want)
			}
			for i := range tt.want {
				if body.ColorOptions[i] != tt.want[i] {
					t.Errorf("color_options[%d] = %+v, want %+v", i, body.ColorOptions[i], tt.want[i])
				}
			}
			assertVaryLanguage(t, rec)
		})
	}
}

func TestProductDetailVariantColorLabel(t *testing.T) {
	src := &fakeDetailSources{variants: map[string][]catalog.Variant{"p1": {
		{ID: "v1", ProductID: "p1", Size: "40", Color: "Чёрный"},
		{ID: "v2", ProductID: "p1", Size: "40", Color: "Терракотовый"},
	}}}
	got, err := buildProductDetails(context.Background(), src, testCfg, i18n.LangKY, []catalog.Product{{ID: "p1"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"color":"Чёрный"`, `"color_label":"Кара"`,
		`"color":"Терракотовый"`, `"color_label":"Терракотовый"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("detail JSON missing %s: %s", want, raw)
		}
	}

	ru, err := buildProductDetails(context.Background(), src, testCfg, i18n.LangRU, []catalog.Product{{ID: "p1"}})
	if err != nil {
		t.Fatal(err)
	}
	if ru[0].Variants[0].ColorLabel != "Чёрный" {
		t.Errorf("ru color_label = %q, want raw color", ru[0].Variants[0].ColorLabel)
	}
}

func TestListFavoritesLocalizesColorAndVaries(t *testing.T) {
	favorites := &fakeFavoriteRepo{ids: []string{"p1"}}
	products := &fakeProductGetter{products: map[string]*catalog.Product{"p1": {ID: "p1"}}}
	details := &fakeDetailSources{variants: map[string][]catalog.Variant{"p1": {{ID: "v1", ProductID: "p1", Color: "Белый"}}}}
	handler := apperr.Wrap(listFavoritesHandler(favorites, products, details, testCfg))

	req := withCustomer(httptest.NewRequest(http.MethodGet, "/api/v1/favorites", nil), "c1")
	req.Header.Set("Accept-Language", "ky")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), `"color_label":"Ак"`) {
		t.Errorf("body = %s, want ky color_label", rec.Body.String())
	}
	assertVaryLanguage(t, rec)
}

func TestListCartLocalizesColor(t *testing.T) {
	cart := &fakeCartService{listResult: []orders.CartLine{
		{VariantID: "v1", Qty: 1, Color: "Бежевый"},
		{VariantID: "v2", Qty: 1, Color: "Терракотовый"},
	}}
	handler := apperr.Wrap(listCartHandler(cart, &fakeCartImageGetter{}, testCfg))

	req := newCustomerRequest(http.MethodGet, "/api/v1/cart", "c1", "")
	req.Header.Set("Accept-Language", "ky")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var lines []cartLineResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &lines); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Color != "Бежевый" || lines[0].ColorLabel != "Беж" || lines[1].ColorLabel != "Терракотовый" {
		t.Errorf("lines = %+v, want raw color kept and ky color_label", lines)
	}
	assertVaryLanguage(t, rec)
}

func sampleOrder() *orders.Order {
	return &orders.Order{ID: "o1", Items: []orders.OrderItem{{ID: "i1", ColorSnapshot: "Чёрный"}}}
}

func TestOrderEndpointsLocalizeItemColor(t *testing.T) {
	fake := &fakeOrderService{
		createResult: sampleOrder(),
		listResult:   []orders.Order{*sampleOrder()},
		getResult:    sampleOrder(),
		cancelResult: sampleOrder(),
	}
	tests := []struct {
		name    string
		handler apperr.HandlerFunc
		method  string
		body    string
	}{
		{"create", createOrderHandler(fake, nil), http.MethodPost, `{"items":[{"variant_id":"v1","quantity":1}]}`},
		{"list", listOrdersHandler(fake), http.MethodGet, ""},
		{"get", getOrderHandler(fake), http.MethodGet, ""},
		{"cancel", cancelOrderHandler(fake), http.MethodPost, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newCustomerRequest(tt.method, "/api/v1/orders/o1", "c1", tt.body)
			req.SetPathValue("id", "o1")
			req.Header.Set("Accept-Language", "ky")
			rec := httptest.NewRecorder()
			apperr.Wrap(tt.handler).ServeHTTP(rec, req)

			body := rec.Body.String()
			if rec.Code >= 300 {
				t.Fatalf("status = %d: %s", rec.Code, body)
			}
			if !strings.Contains(body, `"color_snapshot":"Чёрный"`) || !strings.Contains(body, `"color_label":"Кара"`) {
				t.Errorf("body = %s, want raw color_snapshot and ky color_label", body)
			}
			if strings.Count(body, `"items"`) != 1 {
				t.Errorf("body = %s, want a single items key per order", body)
			}
			assertVaryLanguage(t, rec)
		})
	}
}

func TestOrderItemColorLabelDefaultsToRaw(t *testing.T) {
	out := localizeOrder(sampleOrder(), i18n.LangRU)
	if out.Items[0].ColorLabel != "Чёрный" {
		t.Errorf("ru color_label = %q, want raw", out.Items[0].ColorLabel)
	}
	if localizeOrder(&orders.Order{ID: "o2"}, i18n.LangKY).Items != nil {
		t.Errorf("order without items should keep items nil")
	}
}
