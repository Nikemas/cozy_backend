package payments

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBakaiProviderNotReadyWithoutToken(t *testing.T) {
	b := NewBakaiProvider("", "", "", "", 0, "wh-secret")
	if err := b.Ready(); err == nil {
		t.Fatal("Ready() = nil, want an error without BAKAI_API_TOKEN")
	}
	if _, err := b.CreatePayment(context.Background(), CreateRequest{PaymentID: "p1", ReturnURL: "https://x/pay/return/o1"}); err == nil {
		t.Fatal("CreatePayment succeeded without a token")
	}
}

func TestBakaiProviderCreatePaymentSendsPayLinkAndParsesURL(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/PayLink/CreatePayLink" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		gotBody = string(body)
		_, _ = w.Write([]byte(`"https://pay.bakai.kg/session/abc123"`))
	}))
	defer srv.Close()

	b := NewBakaiProvider(srv.URL, "tok-123", "", "", 0, "wh-secret")
	if err := b.Ready(); err != nil {
		t.Fatalf("Ready() = %v", err)
	}
	sess, err := b.CreatePayment(context.Background(), CreateRequest{
		PaymentID: "pay-1", OrderNumber: "COZY-20260101-001", Amount: 5150,
		ReturnURL: "https://cozy.kg/pay/return/order-1",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if sess.ExternalID != "pay-1" {
		t.Errorf("ExternalID = %q, want the payment id (transactionID echoes back as operationID)", sess.ExternalID)
	}
	if sess.RedirectURL != "https://pay.bakai.kg/session/abc123" {
		t.Errorf("RedirectURL = %q", sess.RedirectURL)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	var req createPayLinkRequest
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if req.TransactionID != "pay-1" || req.Comment != "COZY-20260101-001" || req.Amount != 5150 ||
		req.RedirectURL != "https://cozy.kg/pay/return/order-1" {
		t.Errorf("request body = %+v", req)
	}
}

func TestBakaiProviderCreatePaymentRequiresReturnURL(t *testing.T) {
	b := NewBakaiProvider("https://example.invalid", "tok", "", "", 0, "wh")
	if _, err := b.CreatePayment(context.Background(), CreateRequest{PaymentID: "p1"}); err == nil {
		t.Fatal("CreatePayment without ReturnURL should fail before making any request")
	}
}

func TestBakaiProviderCreatePaymentSurfacesHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("token expired"))
	}))
	defer srv.Close()

	b := NewBakaiProvider(srv.URL, "expired-tok", "", "", 0, "wh")
	_, err := b.CreatePayment(context.Background(), CreateRequest{PaymentID: "p1", ReturnURL: "https://x/r"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("CreatePayment err = %v, want it to surface the 401", err)
	}
}

func TestBakaiProviderParseCallbackRejectsBadToken(t *testing.T) {
	b := NewBakaiProvider("", "tok", "", "", 0, "wh-secret")
	h := http.Header{}
	h.Set(WebhookTokenHeader, "wrong")
	if _, err := b.ParseCallback(h, []byte(`{}`)); err == nil {
		t.Fatal("ParseCallback accepted the wrong token")
	}
}

func TestBakaiProviderParseCallbackMapsStates(t *testing.T) {
	b := NewBakaiProvider("", "tok", "", "", 0, "wh-secret")
	h := http.Header{}
	h.Set(WebhookTokenHeader, "wh-secret")

	cases := []struct {
		state string
		want  Status
	}{
		{"success", StatusPaid},
		{"Processed", StatusPaid},
		{"cancelled", StatusCancelled},
		{"declined", StatusFailed},
		{"", StatusFailed},
	}
	for _, c := range cases {
		body := []byte(`{"operationID":"pay-1","comment":"COZY-1","amount":5150,"operationState":"` + c.state + `"}`)
		ev, err := b.ParseCallback(h, body)
		if err != nil {
			t.Fatalf("state %q: ParseCallback: %v", c.state, err)
		}
		if ev.ExternalID != "pay-1" || ev.Status != c.want || ev.Amount != 5150 {
			t.Errorf("state %q: got ExternalID=%q Status=%q Amount=%v, want pay-1/%q/5150",
				c.state, ev.ExternalID, ev.Status, ev.Amount, c.want)
		}
	}
}

func TestBakaiProviderParseCallbackRejectsMissingOperationID(t *testing.T) {
	b := NewBakaiProvider("", "tok", "", "", 0, "wh-secret")
	h := http.Header{}
	h.Set(WebhookTokenHeader, "wh-secret")
	if _, err := b.ParseCallback(h, []byte(`{"operationState":"success"}`)); err == nil {
		t.Fatal("ParseCallback accepted a callback with no operationID")
	}
}

func TestBakaiProviderGenerateQRDisabledWithoutToken(t *testing.T) {
	b := NewBakaiProvider("", "tok", "", "", 0, "wh")
	if b.QREnabled() {
		t.Fatal("QREnabled() = true without BAKAI_QR_TOKEN")
	}
	if _, err := b.GenerateQR(context.Background(), 100, "pay-1"); err == nil {
		t.Fatal("GenerateQR succeeded without a QR token")
	}
}

func TestBakaiProviderGenerateQRSendsRequestAndParsesLink(t *testing.T) {
	var gotAuth string
	var gotReq generateQRRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/Qr/GenerateQR" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		_ = json.NewEncoder(w).Encode(generateQRResponse{QrLink: "00020101...emvco-payload"})
	}))
	defer srv.Close()

	b := NewBakaiProvider(srv.URL, "tok", "qr-tok", "1234567890", 417, "wh")
	if !b.QREnabled() {
		t.Fatal("QREnabled() = false with a QR token set")
	}
	link, err := b.GenerateQR(context.Background(), 5150, "pay-1")
	if err != nil {
		t.Fatalf("GenerateQR: %v", err)
	}
	if link != "00020101...emvco-payload" {
		t.Errorf("link = %q", link)
	}
	if gotAuth != "Bearer qr-tok" {
		t.Errorf("Authorization = %q, want the QR token, not the pay-link token", gotAuth)
	}
	if gotReq.AccountNo != "1234567890" || gotReq.CurrencyID != 417 || gotReq.Amount != 5150 || gotReq.OperationID != "pay-1" {
		t.Errorf("request = %+v", gotReq)
	}
}

func TestBakaiProviderQueryStatusNotSupported(t *testing.T) {
	b := NewBakaiProvider("", "tok", "", "", 0, "wh")
	if _, err := b.QueryStatus(context.Background(), "pay-1"); err == nil {
		t.Fatal("QueryStatus should report not-configured — Bakai's API has no such endpoint")
	}
}
