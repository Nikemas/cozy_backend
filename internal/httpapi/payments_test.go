package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/payments"
)

// --- POST /api/v1/orders with payment_method ---

type fakeCheckout struct {
	called bool
	order  *orders.Order
	url    string
	image  string
	err    error

	retryCustomer, retryOrder string
}

func (f *fakeCheckout) PlaceOnlineOrder(_ context.Context, _ orders.PlaceOrderInput) (*orders.Order, string, bool, error) {
	f.called = true
	return f.order, f.url, true, f.err
}

func (f *fakeCheckout) RetryPayment(_ context.Context, customerID, orderID string) (string, error) {
	f.called = true
	f.retryCustomer, f.retryOrder = customerID, orderID
	return f.url, f.err
}

func (f *fakeCheckout) GetPaymentQR(_ context.Context, customerID, orderID string) (string, string, error) {
	f.called = true
	f.retryCustomer, f.retryOrder = customerID, orderID
	return f.url, f.image, f.err
}

func TestCreateOrderHandlerOnlineCardReturnsPaymentURL(t *testing.T) {
	pending := orders.PaymentPending
	svc := &fakeOrderService{}
	co := &fakeCheckout{
		order: &orders.Order{ID: "order-1", PaymentMethod: orders.PaymentOnlineCard, PaymentStatus: &pending},
		url:   "https://cozy.test/pay/1",
	}
	handler := apperr.Wrap(createOrderHandler(svc, co))

	body := `{"items":[{"variant_id":"var-1","quantity":1}],"pickup_point_id":"p1","payment_method":"online_card"}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newCustomerRequest(http.MethodPost, "/api/v1/orders", "cust-1", body))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !co.called || svc.createCalled {
		t.Fatalf("online_card must go through checkout only (checkout=%v, cod=%v)", co.called, svc.createCalled)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "order-1" || got["payment_url"] != "https://cozy.test/pay/1" || got["payment_status"] != "pending" {
		t.Errorf("body = %v", got)
	}
}

func TestCreateOrderHandlerCashOnDeliveryHasNoPaymentURL(t *testing.T) {
	svc := &fakeOrderService{createResult: &orders.Order{ID: "order-1", PaymentMethod: orders.PaymentCashOnDelivery}}
	co := &fakeCheckout{}
	handler := apperr.Wrap(createOrderHandler(svc, co))

	body := `{"items":[{"variant_id":"var-1","quantity":1}],"pickup_point_id":"p1","payment_method":"cash_on_delivery"}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newCustomerRequest(http.MethodPost, "/api/v1/orders", "cust-1", body))

	if rec.Code != http.StatusCreated || co.called || !svc.createCalled {
		t.Fatalf("status = %d, checkout = %v, cod = %v", rec.Code, co.called, svc.createCalled)
	}
	if strings.Contains(rec.Body.String(), "payment_url") || strings.Contains(rec.Body.String(), "payment_status") {
		t.Errorf("COD body should have no payment_url/payment_status: %s", rec.Body.String())
	}
}

func TestCreateOrderHandlerRejectsUnknownPaymentMethod(t *testing.T) {
	svc := &fakeOrderService{}
	handler := apperr.Wrap(createOrderHandler(svc, &fakeCheckout{}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newCustomerRequest(http.MethodPost, "/api/v1/orders", "cust-1", `{"items":[],"payment_method":"online"}`))

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_payment_method") {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if svc.createCalled {
		t.Fatal("no order should be created")
	}
}

func TestCreateOrderHandlerOnlineCardWithoutCheckoutIs503(t *testing.T) {
	handler := apperr.Wrap(createOrderHandler(&fakeOrderService{}, nil))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newCustomerRequest(http.MethodPost, "/api/v1/orders", "cust-1", `{"items":[],"payment_method":"online_card"}`))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}

// --- webhook + mock checkout ---

type fakePaymentBackend struct {
	provider string
	header   http.Header
	body     string
	res      *payments.CallbackResult
	err      error
	payment  *payments.Payment
}

func (f *fakePaymentBackend) HandleCallback(_ context.Context, provider string, header http.Header, body []byte) (*payments.CallbackResult, error) {
	f.provider, f.header, f.body = provider, header, string(body)
	return f.res, f.err
}

func (f *fakePaymentBackend) GetByExternalID(_ context.Context, _, _ string) (*payments.Payment, error) {
	if f.payment == nil {
		return nil, apperr.NotFound("payment_not_found", "платёж не найден")
	}
	return f.payment, nil
}

func TestPaymentCallbackHandlerPassesProviderHeaderAndBody(t *testing.T) {
	fake := &fakePaymentBackend{res: &payments.CallbackResult{PaymentID: "pay-1", Status: payments.StatusPaid, Applied: true}}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/payments/{provider}/callback", apperr.Wrap(paymentCallbackHandler(fake, "")))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/mock/callback", strings.NewReader(`{"x":1}`))
	req.Header.Set(payments.WebhookTokenHeader, "tok")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if fake.provider != "mock" || fake.body != `{"x":1}` || fake.header.Get(payments.WebhookTokenHeader) != "tok" {
		t.Errorf("got provider=%q body=%q header=%v", fake.provider, fake.body, fake.header)
	}
	if !strings.Contains(rec.Body.String(), `"applied":true`) {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestPaymentCallbackHandlerFixedProviderAndError(t *testing.T) {
	fake := &fakePaymentBackend{err: apperr.Unauthorized("invalid_webhook_token", "неверный токен вебхука")}
	handler := apperr.Wrap(paymentCallbackHandler(fake, payments.BakaiProviderName))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/payments/bakai/webhook", strings.NewReader(`{}`)))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if fake.provider != payments.BakaiProviderName {
		t.Errorf("provider = %q, want bakai", fake.provider)
	}
}

func TestMockCheckoutConfirmSendsSignedCallback(t *testing.T) {
	mock := payments.NewMockProvider("https://cozy.test", "tok")
	fake := &fakePaymentBackend{
		payment: &payments.Payment{OrderID: "order-uuid-1", OrderNumber: "COZY-1", Amount: 4990.5, Currency: "KGS", Status: payments.StatusPending},
		res:     &payments.CallbackResult{Status: payments.StatusPaid, Applied: true},
	}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/payments/mock/checkout/{externalId}", apperr.Wrap(mockCheckoutConfirmHandler(fake, mock, "https://cozy.test")))

	form := url.Values{"result": {"paid"}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/mock/checkout/mock_1", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if fake.provider != payments.MockProviderName {
		t.Errorf("provider = %q", fake.provider)
	}
	// The callback it built must pass the real mock verification.
	ev, err := mock.ParseCallback(fake.header, []byte(fake.body))
	if err != nil {
		t.Fatalf("mock page produced an unverifiable callback: %v", err)
	}
	if ev.ExternalID != "mock_1" || ev.Status != payments.StatusPaid || ev.Amount != 4990.5 {
		t.Errorf("event = %+v", ev)
	}
	// Back to the site's payment result page, like a real bank redirect.
	if !strings.Contains(rec.Body.String(), `href="https://cozy.test/pay/return/order-uuid-1"`) ||
		!strings.Contains(rec.Body.String(), `content="2;url=https://cozy.test/pay/return/order-uuid-1"`) {
		t.Errorf("result page lacks the /pay/return link/redirect: %s", rec.Body.String())
	}
}

func TestMockCheckoutConfirmRejectsBadResult(t *testing.T) {
	fake := &fakePaymentBackend{payment: &payments.Payment{OrderNumber: "COZY-1"}}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/payments/mock/checkout/{externalId}",
		apperr.Wrap(mockCheckoutConfirmHandler(fake, payments.NewMockProvider("", "tok"), "")))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/payments/mock/checkout/mock_1?result=refunded", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	if fake.provider != "" {
		t.Error("no callback should be sent for an invalid result")
	}
}

func TestMockCheckoutPageRendersPendingButtons(t *testing.T) {
	fake := &fakePaymentBackend{payment: &payments.Payment{OrderNumber: "COZY-7", Amount: 100, Currency: "KGS", Status: payments.StatusPending}}
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/payments/mock/checkout/{externalId}", apperr.Wrap(mockCheckoutPageHandler(fake, "")))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/payments/mock/checkout/mock_1", nil))

	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "COZY-7") || !strings.Contains(body, "Оплатить") || !strings.Contains(body, "100.00 KGS") {
		t.Fatalf("status = %d, body = %s", rec.Code, body)
	}
}

func TestRegisterPaymentRoutesMockPageOnlyForMock(t *testing.T) {
	for _, p := range []payments.Provider{payments.NewMockProvider("", "tok"), payments.NewBakaiProvider("", "", "", "", 0, "tok")} {
		mux := http.NewServeMux()
		RegisterPaymentRoutes(mux, payments.NewService(nil, p, nil, ""), "")
		_, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, "/api/v1/payments/mock/checkout/x", nil))
		isMock := p.Name() == payments.MockProviderName
		if (pattern != "") != isMock {
			t.Errorf("provider %s: mock checkout route registered = %v, want %v", p.Name(), pattern != "", isMock)
		}
	}
}

// --- POST /api/v1/orders/{id}/pay ---

func TestPayOrderHandlerReturnsPaymentURL(t *testing.T) {
	co := &fakeCheckout{url: "https://cozy.test/pay/2"}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/orders/{id}/pay", apperr.Wrap(payOrderHandler(co)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newCustomerRequest(http.MethodPost, "/api/v1/orders/order-1/pay", "cust-1", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["payment_url"] != "https://cozy.test/pay/2" {
		t.Errorf("body = %v, want only payment_url", got)
	}
	if co.retryCustomer != "cust-1" || co.retryOrder != "order-1" {
		t.Errorf("RetryPayment(%q, %q)", co.retryCustomer, co.retryOrder)
	}
}

func TestPayOrderHandlerNotRetryableIs409(t *testing.T) {
	co := &fakeCheckout{err: orders.ErrPaymentNotRetryable}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/orders/{id}/pay", apperr.Wrap(payOrderHandler(co)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newCustomerRequest(http.MethodPost, "/api/v1/orders/order-1/pay", "cust-1", ""))

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "payment_not_retryable") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestPayOrderHandlerWithoutCheckoutIs503(t *testing.T) {
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/orders/{id}/pay", apperr.Wrap(payOrderHandler(nil)))
	mux.ServeHTTP(rec, newCustomerRequest(http.MethodPost, "/api/v1/orders/order-1/pay", "cust-1", ""))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestPayOrderQRHandlerReturnsQR(t *testing.T) {
	co := &fakeCheckout{url: "00020101...emvco", image: "data:image/png;base64,abc"}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/orders/{id}/pay/qr", apperr.Wrap(payOrderQRHandler(co)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newCustomerRequest(http.MethodPost, "/api/v1/orders/order-1/pay/qr", "cust-1", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["qr_link"] != "00020101...emvco" || got["qr_image"] != "data:image/png;base64,abc" {
		t.Errorf("body = %v", got)
	}
	if co.retryCustomer != "cust-1" || co.retryOrder != "order-1" {
		t.Errorf("GetPaymentQR(%q, %q)", co.retryCustomer, co.retryOrder)
	}
}

func TestPayOrderQRHandlerNotConfiguredIs501(t *testing.T) {
	co := &fakeCheckout{err: payments.ErrQRNotConfigured}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/orders/{id}/pay/qr", apperr.Wrap(payOrderQRHandler(co)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newCustomerRequest(http.MethodPost, "/api/v1/orders/order-1/pay/qr", "cust-1", ""))

	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "qr_not_configured") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestPayOrderQRHandlerWithoutCheckoutIs503(t *testing.T) {
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/orders/{id}/pay/qr", apperr.Wrap(payOrderQRHandler(nil)))
	mux.ServeHTTP(rec, newCustomerRequest(http.MethodPost, "/api/v1/orders/order-1/pay/qr", "cust-1", ""))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
}
