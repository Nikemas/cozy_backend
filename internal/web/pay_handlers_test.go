package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/payments"
)

const testOrderID = "44444444-4444-4444-8444-444444444444"

func onlineOrderFor(status orders.OrderStatus, ps orders.PaymentStatus) *orders.Order {
	return &orders.Order{ID: testOrderID, OrderNumber: "COZY-20260924-001", Status: status,
		PaymentMethod: orders.PaymentOnlineCard, PaymentStatus: &ps, TotalAmount: 5200}
}

func TestBuildPayReturnData(t *testing.T) {
	t.Run("anonymous visitor gets only the app link", func(t *testing.T) {
		d := buildPayReturnData(testOrderID, nil, 0, false, false)
		if !d.Anonymous || !d.ShowAppLink || d.AppLink != "cozy://orders/"+testOrderID || d.Poll || d.Retryable {
			t.Errorf("anonymous = %+v", d)
		}
	})
	t.Run("malformed id has no app link", func(t *testing.T) {
		d := buildPayReturnData(`x"><script>`, nil, 0, true, false)
		if d.AppLink != "" || d.ShowAppLink {
			t.Errorf("AppLink = %q, ShowAppLink = %v", d.AppLink, d.ShowAppLink)
		}
	})
	t.Run("pending polls, then times out with retry", func(t *testing.T) {
		d := buildPayReturnData(testOrderID, onlineOrderFor(orders.StatusPlaced, orders.PaymentPending), 3, false, false)
		if d.State != payStatePending || !d.Poll || d.Retryable || d.PollURL != "/pay/return/"+testOrderID+"/status?n=4" {
			t.Errorf("pending = %+v", d)
		}
		d = buildPayReturnData(testOrderID, onlineOrderFor(orders.StatusPlaced, orders.PaymentPending), payPollMax, false, false)
		if d.Poll || !d.TimedOut || !d.Retryable {
			t.Errorf("timed out = %+v", d)
		}
	})
	t.Run("failed offers retry, no polling", func(t *testing.T) {
		d := buildPayReturnData(testOrderID, onlineOrderFor(orders.StatusPlaced, orders.PaymentFailed), 0, true, false)
		if d.State != payStateFailed || d.Poll || !d.Retryable || !d.ShowAppLink {
			t.Errorf("failed = %+v", d)
		}
	})
	t.Run("paid and cancelled stop", func(t *testing.T) {
		d := buildPayReturnData(testOrderID, onlineOrderFor(orders.StatusPlaced, orders.PaymentPaid), 0, false, false)
		if d.State != payStatePaid || d.Poll || d.Retryable {
			t.Errorf("paid = %+v", d)
		}
		d = buildPayReturnData(testOrderID, onlineOrderFor(orders.StatusCancelled, orders.PaymentCancelled), 0, false, false)
		if d.State != payStateCancelled || d.Poll || d.Retryable {
			t.Errorf("cancelled = %+v", d)
		}
	})
	t.Run("QR offered only when retryable and the provider supports it", func(t *testing.T) {
		failed := onlineOrderFor(orders.StatusPlaced, orders.PaymentFailed)
		if d := buildPayReturnData(testOrderID, failed, 0, false, true); !d.QROffered {
			t.Errorf("retryable + qrAvailable must offer QR: %+v", d)
		}
		if d := buildPayReturnData(testOrderID, failed, 0, false, false); d.QROffered {
			t.Errorf("qrAvailable=false must not offer QR: %+v", d)
		}
		paid := onlineOrderFor(orders.StatusPlaced, orders.PaymentPaid)
		if d := buildPayReturnData(testOrderID, paid, 0, false, true); d.QROffered {
			t.Errorf("not retryable (already paid) must not offer QR even if qrAvailable: %+v", d)
		}
	})
}

func TestRenderPayReturnExecutes(t *testing.T) {
	rr := newTestRenderer(t)
	cases := []struct {
		name  string
		data  PayReturnData
		wants []string
		not   []string
	}{
		{"anonymous", buildPayReturnData(testOrderID, nil, 0, false, false),
			[]string{`href="cozy://orders/` + testOrderID + `"`, "Вернуться в приложение"}, []string{"hx-get", "COZY-"}},
		{"pending", buildPayReturnData(testOrderID, onlineOrderFor(orders.StatusPlaced, orders.PaymentPending), 0, false, false),
			[]string{`hx-get="/pay/return/` + testOrderID + `/status?n=1"`, `hx-trigger="every 2s"`, "COZY-20260924-001"}, []string{"/retry"}},
		{"failed", buildPayReturnData(testOrderID, onlineOrderFor(orders.StatusPlaced, orders.PaymentFailed), 0, true, false),
			[]string{`action="/pay/` + testOrderID + `/retry"`, "Оплатить снова", "cozy://orders/"}, []string{"hx-get", "Показать QR"}},
		{"failed_with_qr", buildPayReturnData(testOrderID, onlineOrderFor(orders.StatusPlaced, orders.PaymentFailed), 0, true, true),
			[]string{`hx-post="/pay/` + testOrderID + `/qr"`, "Показать QR", `id="pay-qr"`}, nil},
		{"paid", buildPayReturnData(testOrderID, onlineOrderFor(orders.StatusPlaced, orders.PaymentPaid), 0, false, false),
			[]string{"Оплата прошла"}, []string{"hx-get", "/retry", "cozy://", "Показать QR"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, partial := range []bool{false, true} {
				w := httptest.NewRecorder()
				data := PageData{Lang: i18n.LangRU, Screen: "pay_return", Authed: !c.data.Anonymous, Data: c.data}
				var err error
				if partial {
					err = rr.RenderPartial(w, "pay_return", "pay_status", data)
				} else {
					err = rr.Render(w, "pay_return", data)
				}
				if err != nil {
					t.Fatalf("render (partial=%v): %v", partial, err)
				}
				body := w.Body.String()
				for _, s := range c.wants {
					if !strings.Contains(body, s) {
						t.Errorf("partial=%v: body lacks %q", partial, s)
					}
				}
				for _, s := range c.not {
					if strings.Contains(body, s) {
						t.Errorf("partial=%v: body must not contain %q", partial, s)
					}
				}
			}
		})
	}
}

func TestRenderPayQRFragment(t *testing.T) {
	rr := newTestRenderer(t)
	cases := []struct {
		name  string
		data  PayQRData
		wants []string
	}{
		{"image", PayQRData{OrderID: testOrderID, QRLink: "00020101...emvco", QRImage: "data:image/png;base64,abc"},
			[]string{`src="data:image/png;base64,abc"`}},
		{"link only", PayQRData{OrderID: testOrderID, QRLink: "00020101...emvco"},
			[]string{"00020101...emvco"}},
		{"error", PayQRData{OrderID: testOrderID, Error: "оплата по QR пока недоступна"},
			[]string{"оплата по QR пока недоступна"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			data := PageData{Lang: i18n.LangRU, Screen: "pay_return", Authed: true, Data: c.data}
			if err := rr.RenderPartial(w, "pay_return", "pay_qr", data); err != nil {
				t.Fatalf("render: %v", err)
			}
			body := w.Body.String()
			for _, s := range c.wants {
				if !strings.Contains(body, s) {
					t.Errorf("body lacks %q: %s", s, body)
				}
			}
		})
	}
}

func TestCheckoutZonesAndInitialTotals(t *testing.T) {
	free := 6000.0
	zones := checkoutZones([]orders.DeliveryZone{
		{ID: "z1", NameRu: "Бишкек", NameKy: "Бишкек ш.", Fee: 200, FreeFrom: &free},
		{ID: "z2", NameRu: "Пригород", NameKy: "Чет", Fee: 400},
	}, 6000, i18n.LangKY)
	if zones[0].Fee != 0 || zones[0].Total != 6000 || !zones[0].HasFreeFrom || zones[0].Name != "Бишкек ш." {
		t.Errorf("zone 0 = %+v (free from 6000 → 0)", zones[0])
	}
	if zones[1].Fee != 400 || zones[1].Total != 6400 || zones[1].HasFreeFrom {
		t.Errorf("zone 1 = %+v", zones[1])
	}

	d := CheckoutPageData{ItemsTotal: 5000, DeliveryFee: 250, Addresses: []AddressView{{ID: "a"}}}
	if d.InitialFee() != 250 || d.InitialTotal() != 5250 || d.FlatTotal() != 5250 {
		t.Errorf("flat: %v/%v/%v", d.InitialFee(), d.InitialTotal(), d.FlatTotal())
	}
	d.Zones = []CheckoutZoneView{{ID: "z", Fee: 300, Total: 5300}}
	if d.InitialFee() != 300 || d.InitialTotal() != 5300 {
		t.Errorf("zoned: %v/%v", d.InitialFee(), d.InitialTotal())
	}
	d.Addresses = nil // pickup preselected
	if d.InitialFee() != 0 || d.InitialTotal() != 5000 {
		t.Errorf("pickup: %v/%v", d.InitialFee(), d.InitialTotal())
	}
}

func TestRenderCheckoutWithZonesAndOnlinePayment(t *testing.T) {
	rr := newTestRenderer(t)
	free := 10000.0
	data := CheckoutPageData{
		Addresses:     []AddressView{{ID: "a1", AddressText: "ул. Ленина 1"}},
		Points:        []PointView{{ID: "p1", Name: "Cozy", Address: "ул. Чуй 1"}},
		Zones:         checkoutZones([]orders.DeliveryZone{{ID: "z1", NameRu: "Бишкек", Fee: 300, FreeFrom: &free}}, 5000, i18n.LangRU),
		ItemsTotal:    5000,
		ItemCount:     1,
		DeliveryFee:   200,
		OnlinePayment: true,
	}
	w := httptest.NewRecorder()
	if err := rr.Render(w, "checkout", PageData{Lang: i18n.LangRU, Screen: "checkout", Authed: true, Data: data}); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	for _, s := range []string{`name="delivery_zone_id"`, `value="z1"`, `name="payment_method" value="online_card"`,
		"Картой онлайн", "бесплатно от 10", `id="checkout-total"`, "5 300"} {
		if !strings.Contains(body, s) {
			t.Errorf("checkout page lacks %q", s)
		}
	}

	// No provider → no card option.
	data.OnlinePayment = false
	w = httptest.NewRecorder()
	if err := rr.Render(w, "checkout", PageData{Lang: i18n.LangRU, Screen: "checkout", Authed: true, Data: data}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), `value="online_card"`) {
		t.Error("card online offered without a payment provider")
	}
}

func TestApplyZoneDelivery(t *testing.T) {
	page := &CartPageData{Lines: []CartLineView{{VariantID: "v"}}, ItemsTotal: 5000, DeliveryFee: 200, GrandTotal: 5200}
	applyZoneDelivery(page, nil)
	if page.DeliveryFee != 200 || page.DeliveryFrom {
		t.Errorf("no zones must keep the flat fee: %+v", page)
	}
	free := 5000.0
	applyZoneDelivery(page, []orders.DeliveryZone{{Fee: 300}, {Fee: 150}, {Fee: 500, FreeFrom: &free}})
	if page.DeliveryFee != 0 || page.GrandTotal != 5000 || !page.DeliveryFrom {
		t.Errorf("cheapest zone (free from 5000) → 0, from: %+v", page)
	}
	page = &CartPageData{Lines: []CartLineView{{VariantID: "v"}}, ItemsTotal: 1000}
	applyZoneDelivery(page, []orders.DeliveryZone{{Fee: 250}})
	if page.DeliveryFee != 250 || page.GrandTotal != 1250 || page.DeliveryFrom {
		t.Errorf("single zone is exact: %+v", page)
	}

	rr := newTestRenderer(t)
	w := httptest.NewRecorder()
	page.DeliveryFrom = true
	if err := rr.Render(w, "cart", PageData{Lang: i18n.LangRU, Screen: "cart", Authed: true, Data: page}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.Body.String(), "от 250") {
		t.Errorf("cart with differing zones should say «от …»")
	}
}

func TestOrderLandingPath(t *testing.T) {
	if got := orderLandingPath(onlineOrderFor(orders.StatusPlaced, orders.PaymentPending)); got != "/pay/return/"+testOrderID {
		t.Errorf("online → %q", got)
	}
	if got := orderLandingPath(&orders.Order{OrderNumber: "COZY-1", PaymentMethod: orders.PaymentCashOnDelivery}); got != "/order/COZY-1/done" {
		t.Errorf("cash → %q", got)
	}
}

// --- "Купить сейчас" ---

const (
	buyProductID = "55555555-5555-4555-8555-555555555555"
	buyVariantID = "66666666-6666-4666-8666-666666666666"
)

func buyNowRequest() *http.Request {
	form := url.Values{"size": {"42"}, "color": {"black"}}
	r := httptest.NewRequest(http.MethodPost, "/product/"+buyProductID+"-air-max/buy", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Request", "true")
	r.SetPathValue("slug", buyProductID+"-air-max")
	return r.WithContext(context.WithValue(r.Context(), customerIDKey, "cust-1"))
}

func expectBuyNowVariant(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("FROM product_variants")).WithArgs(buyProductID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "product_id", "size", "color", "sku", "price_override"}).
			AddRow(buyVariantID, buyProductID, "42", "black", "SKU-42", nil))
}

// TestProductBuyNowAddsToCartAndGoesToCheckout: "Купить сейчас" puts the
// variant in the cart and sends the customer to /checkout (it used to
// place an order with no fulfillment and always fail).
func TestProductBuyNowAddsToCartAndGoesToCheckout(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	h := &handlers{variants: catalog.NewVariantRepo(db), cartRepo: orders.NewCartRepo(db)}

	expectBuyNowVariant(mock)
	mock.ExpectQuery(regexp.QuoteMeta("FROM cart_items")).WithArgs("cust-1").
		WillReturnRows(sqlmock.NewRows([]string{"customer_id", "variant_id", "qty", "created_at"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT p.is_active")).WithArgs(buyVariantID).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO cart_items")).WithArgs("cust-1", buyVariantID, 1, orders.MaxCartQty).
		WillReturnResult(sqlmock.NewResult(0, 1))

	rec := httptest.NewRecorder()
	apperr.Wrap(h.productBuyNow).ServeHTTP(rec, buyNowRequest())
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/checkout" {
		t.Fatalf("status = %d, HX-Redirect = %q: %s", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}

	// Already in the cart: no second pair.
	expectBuyNowVariant(mock)
	mock.ExpectQuery(regexp.QuoteMeta("FROM cart_items")).WithArgs("cust-1").
		WillReturnRows(sqlmock.NewRows([]string{"customer_id", "variant_id", "qty", "created_at"}).
			AddRow("cust-1", buyVariantID, 1, time.Now()))
	rec = httptest.NewRecorder()
	apperr.Wrap(h.productBuyNow).ServeHTTP(rec, buyNowRequest())
	if rec.Header().Get("HX-Redirect") != "/checkout" {
		t.Fatalf("second click: HX-Redirect = %q", rec.Header().Get("HX-Redirect"))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestPayReturnRouteServesAnonymousPage(t *testing.T) {
	mux := newTestMux(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pay/return/"+testOrderID, nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "cozy://orders/"+testOrderID) {
		t.Fatalf("status = %d, body lacks the app link: %s", rec.Code, body)
	}
	if !strings.Contains(body, `name="robots"`) {
		t.Error("payment result page must be noindex")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if payments.ReturnPath(testOrderID) != "/pay/return/"+testOrderID {
		t.Error("route and payments.ReturnPath disagree")
	}
}
