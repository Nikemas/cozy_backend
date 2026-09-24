package httpapi

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/payments"
)

// maxCallbackBody caps a provider callback body; real ones are a few KB.
const maxCallbackBody = 64 << 10

// callbackHandler is the subset of *payments.Service the webhook needs.
type callbackHandler interface {
	HandleCallback(ctx context.Context, providerName string, header http.Header, body []byte) (*payments.CallbackResult, error)
}

// mockCheckoutBackend is what the mock checkout page needs.
type mockCheckoutBackend interface {
	callbackHandler
	GetByExternalID(ctx context.Context, provider, externalID string) (*payments.Payment, error)
}

// RegisterPaymentRoutes mounts the provider webhook (unauthenticated —
// the provider authenticates via payments.WebhookTokenHeader) and, only
// while the mock provider is active, its stand-in checkout page.
//
//	POST /api/v1/payments/{provider}/callback
//	POST /api/v1/payments/bakai/webhook          (path named in ТЗ §6, same handler)
//	GET  /api/v1/payments/mock/checkout/{externalId}   (mock only)
//	POST /api/v1/payments/mock/checkout/{externalId}   (mock only; result=paid|failed|cancelled)
func RegisterPaymentRoutes(mux *http.ServeMux, svc *payments.Service, publicBaseURL string) {
	mux.Handle("POST /api/v1/payments/{provider}/callback", apperr.Wrap(paymentCallbackHandler(svc, "")))
	mux.Handle("POST /api/v1/payments/bakai/webhook", apperr.Wrap(paymentCallbackHandler(svc, payments.BakaiProviderName)))

	if mock, ok := svc.Provider().(*payments.MockProvider); ok {
		mux.Handle("GET /api/v1/payments/mock/checkout/{externalId}", apperr.Wrap(mockCheckoutPageHandler(svc, publicBaseURL)))
		mux.Handle("POST /api/v1/payments/mock/checkout/{externalId}", apperr.Wrap(mockCheckoutConfirmHandler(svc, mock, publicBaseURL)))
	}
}

// paymentCallbackHandler serves the provider webhook. fixedProvider, when
// set, overrides the {provider} path value (for the ТЗ's /bakai/webhook
// alias). Responds 200 with the CallbackResult for handled, duplicate and
// ignored events alike, so the provider stops retrying; errors (bad token,
// unknown payment, amount mismatch) are non-2xx.
func paymentCallbackHandler(svc callbackHandler, fixedProvider string) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		provider := fixedProvider
		if provider == "" {
			provider = r.PathValue("provider")
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCallbackBody))
		if err != nil {
			return apperr.BadRequest("bad_request", "некорректное тело запроса")
		}
		res, err := svc.HandleCallback(r.Context(), provider, r.Header, body)
		if err != nil {
			var appErr *apperr.AppError
			if errors.As(err, &appErr) {
				slog.Warn("payments: callback rejected", "provider", provider, "code", appErr.Code)
			}
			return err
		}
		return writeJSON(w, http.StatusOK, res)
	}
}

var mockCheckoutTmpl = template.Must(template.New("mock").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
{{if .Result}}<meta http-equiv="refresh" content="2;url={{.ReturnURL}}">{{end}}
<title>Тестовая оплата — Cozy</title>
<style>
body{font-family:system-ui,sans-serif;max-width:420px;margin:40px auto;padding:0 16px;color:#222}
.warn{background:#fff4d6;border:1px solid #e6c65c;padding:10px 12px;border-radius:8px;font-size:14px}
.sum{font-size:28px;font-weight:600;margin:16px 0}
button{display:block;width:100%;padding:14px;margin:10px 0;border:0;border-radius:10px;font-size:16px;cursor:pointer}
.pay{background:#1f7a3a;color:#fff}.fail{background:#b3261e;color:#fff}.cancel{background:#eee}
</style></head><body>
<p class="warn">Тестовый режим: реальная оплата не производится (провайдер <b>mock</b>, Bakai ещё не подключён).</p>
<h1>Заказ {{.P.OrderNumber}}</h1>
<div class="sum">{{printf "%.2f" .P.Amount}} {{.P.Currency}}</div>
{{if .Result}}
<p>Статус оплаты: <b>{{.Result}}</b></p>
<p><a href="{{.ReturnURL}}">Вернуться в магазин</a> (автоматически через 2 секунды)</p>
{{else if eq (print .P.Status) "pending"}}
<form method="post"><input type="hidden" name="result" value="paid"><button class="pay">Оплатить</button></form>
<form method="post"><input type="hidden" name="result" value="failed"><button class="fail">Отказ банка</button></form>
<form method="post"><input type="hidden" name="result" value="cancelled"><button class="cancel">Отменить оплату</button></form>
{{else}}
<p>Платёж уже обработан: <b>{{.P.Status}}</b></p>
<p><a href="{{.ReturnURL}}">Вернуться в магазин</a></p>
{{end}}
</body></html>`))

type mockCheckoutView struct {
	P         *payments.Payment
	Result    string
	ReturnURL string
}

func renderMockCheckout(w http.ResponseWriter, v mockCheckoutView) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	return mockCheckoutTmpl.Execute(w, v)
}

// mockReturnURL is where the mock "bank" sends the customer back — the
// same /pay/return/<order_id> page a real provider returns to.
func mockReturnURL(publicBaseURL string, p *payments.Payment) string {
	return publicBaseURL + payments.ReturnPath(p.OrderID)
}

func mockCheckoutPageHandler(svc mockCheckoutBackend, publicBaseURL string) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		p, err := svc.GetByExternalID(r.Context(), payments.MockProviderName, r.PathValue("externalId"))
		if err != nil {
			return err
		}
		return renderMockCheckout(w, mockCheckoutView{P: p, ReturnURL: mockReturnURL(publicBaseURL, p)})
	}
}

// mockCheckoutConfirmHandler plays the bank: it signs a callback for the
// chosen result and feeds it through the real webhook path
// (Service.HandleCallback), then shows the outcome. result comes from the
// form (or ?result= for scripted tests: curl -X POST ...?result=paid).
func mockCheckoutConfirmHandler(svc mockCheckoutBackend, mock *payments.MockProvider, publicBaseURL string) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		externalID := r.PathValue("externalId")
		p, err := svc.GetByExternalID(r.Context(), payments.MockProviderName, externalID)
		if err != nil {
			return err
		}
		status := payments.Status(r.FormValue("result"))
		if status != payments.StatusPaid && status != payments.StatusFailed && status != payments.StatusCancelled {
			return apperr.BadRequest("invalid_result", "result должен быть paid, failed или cancelled")
		}
		header, body, err := mock.SignedCallback(externalID, status, p.Amount)
		if err != nil {
			return err
		}
		res, err := svc.HandleCallback(r.Context(), payments.MockProviderName, header, body)
		if err != nil {
			return err
		}
		result := string(res.Status)
		if !res.Applied {
			result = fmt.Sprintf("%s (без изменений)", res.Status)
		}
		return renderMockCheckout(w, mockCheckoutView{P: p, Result: result, ReturnURL: mockReturnURL(publicBaseURL, p)})
	}
}
