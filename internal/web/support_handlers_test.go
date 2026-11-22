package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/support"
)

// fakeWebSupport is a supportService for guest-path handler tests
// (logged-in paths need the database for base() and are covered by the
// integration tests).
type fakeWebSupport struct {
	created []support.CreateInput
	err     error
}

func (f *fakeWebSupport) Create(_ context.Context, in support.CreateInput) (*support.Ticket, error) {
	f.created = append(f.created, in)
	if f.err != nil {
		return nil, f.err
	}
	return &support.Ticket{ID: "t1", Number: 17}, nil
}

func (f *fakeWebSupport) ListForCustomer(context.Context, string, int, int) ([]support.Summary, int, error) {
	return nil, 0, nil
}

func (f *fakeWebSupport) GetForCustomer(context.Context, string, string) (*support.Ticket, error) {
	return nil, apperr.NotFound("ticket_not_found", "обращение не найдено")
}

func (f *fakeWebSupport) AddCustomerMessage(context.Context, string, string, string) (*support.Ticket, error) {
	return nil, apperr.NotFound("ticket_not_found", "обращение не найдено")
}

func newSupportTestHandlers(t *testing.T, svc *fakeWebSupport) *handlers {
	t.Helper()
	h := newTestHandlers(t)
	h.support = svc
	h.supportGuests = newSupportGuestLimiter()
	return h
}

func postSupport(t *testing.T, h *handlers, form url.Values, remote string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/support", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = remote
	return serveWithErrors(t, h, h.supportSubmit, req)
}

func guestForm() url.Values {
	return url.Values{"name": {"Гость"}, "phone": {"0700 123 456"}, "topic": {"delivery"}, "message": {"Когда привезут?"}}
}

func TestSupportFormForGuestAsksNameAndPhone(t *testing.T) {
	h := newSupportTestHandlers(t, &fakeWebSupport{})
	w := serveWithErrors(t, h, h.supportForm, httptest.NewRequest(http.MethodGet, "/support", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`action="/support"`, `name="name"`, `name="phone"`, `name="topic"`, `name="message"`,
		`value="return"`, "Возврат / обмен", `<meta name="robots" content="noindex">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %q", want)
		}
	}
	if strings.Contains(body, `name="order_id"`) {
		t.Error("guest form must not offer an order select")
	}
	if strings.Contains(body, "Мои обращения") {
		t.Error("guest sees a request list")
	}
}

func TestSupportGuestSubmitRedirectsWithNumber(t *testing.T) {
	svc := &fakeWebSupport{}
	h := newSupportTestHandlers(t, svc)
	w := postSupport(t, h, guestForm(), "10.0.0.1:1234")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/support?sent=17" {
		t.Fatalf("status=%d location=%q", w.Code, w.Header().Get("Location"))
	}
	in := svc.created[0]
	if in.CustomerID != "" || in.Channel != support.ChannelWeb || in.Name != "Гость" || in.Topic != support.TopicDelivery {
		t.Fatalf("input = %+v", in)
	}

	// The "sent" note after the redirect.
	w = serveWithErrors(t, h, h.supportForm, httptest.NewRequest(http.MethodGet, "/support?sent=17", nil))
	if !strings.Contains(w.Body.String(), "Обращение №17 принято") {
		t.Error("sent note missing")
	}
}

func TestSupportGuestValidationErrorKeepsInput(t *testing.T) {
	svc := &fakeWebSupport{err: apperr.BadRequest("name_required", "укажите имя")}
	h := newSupportTestHandlers(t, svc)
	form := guestForm()
	form.Set("name", "")
	w := postSupport(t, h, form, "10.0.0.2:1234")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "укажите имя") || !strings.Contains(body, "Когда привезут?") || !strings.Contains(body, `value="delivery" selected`) {
		t.Errorf("error page lost the error or the input:\n%s", body)
	}
}

func TestSupportGuestRateLimitedPerIP(t *testing.T) {
	h := newSupportTestHandlers(t, &fakeWebSupport{})
	for i := 0; i < supportGuestPerHour; i++ {
		if w := postSupport(t, h, guestForm(), "10.0.0.3:1"); w.Code != http.StatusSeeOther {
			t.Fatalf("request %d: status %d", i+1, w.Code)
		}
	}
	w := postSupport(t, h, guestForm(), "10.0.0.3:1")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "Слишком много обращений") {
		t.Fatalf("over limit: status %d", w.Code)
	}
	// Another IP is unaffected.
	if w := postSupport(t, h, guestForm(), "10.0.0.4:1"); w.Code != http.StatusSeeOther {
		t.Fatalf("other IP: status %d", w.Code)
	}
}

func TestSupportTicketPageNeedsLogin(t *testing.T) {
	h := newSupportTestHandlers(t, &fakeWebSupport{})
	req := httptest.NewRequest(http.MethodGet, "/support/abc", nil)
	req.SetPathValue("id", "abc")
	w := serveWithErrors(t, h, h.supportTicket, req)
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/profile") {
		t.Fatalf("status=%d location=%q", w.Code, w.Header().Get("Location"))
	}
}
