package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// fakeDeleter records DeleteCustomer calls and returns err.
type fakeDeleter struct {
	calls []string
	err   error
}

func (f *fakeDeleter) DeleteCustomer(_ context.Context, customerID string) error {
	f.calls = append(f.calls, customerID)
	return f.err
}

func postAccountDelete(t *testing.T, h *handlers, customerID string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/account/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if customerID != "" {
		req = req.WithContext(context.WithValue(req.Context(), customerIDKey, customerID))
	}
	w := httptest.NewRecorder()
	if err := h.accountDelete(w, req); err != nil {
		t.Fatalf("accountDelete: %v", err)
	}
	return w
}

func confirmedForm() url.Values { return url.Values{"confirm": {deleteConfirmValue}} }

func sessionCookieCleared(w *httptest.ResponseRecorder) bool {
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName && c.MaxAge < 0 && c.Value == "" {
			return true
		}
	}
	return false
}

func TestAccountDeletionPageServesBothLanguages(t *testing.T) {
	mux := newTestMuxWithConfig(t, filledContactsConfig())
	want := map[string][]string{
		i18n.LangRU: {"<h1>Удаление аккаунта Cozy</h1>", "Профиль", "Удалить аккаунт", "Войти и удалить аккаунт", `href="mailto:help@cozy.test"`},
		i18n.LangKY: {"<h1>Cozy аккаунтун өчүрүү</h1>", "Профиль", "Аккаунтту өчүрүү", "Кирип, аккаунтту өчүрүү", `href="mailto:help@cozy.test"`},
	}
	for lang, fragments := range want {
		body := getStaticPage(t, mux, accountDeletionPage, lang)
		if strings.Contains(body, unfilledMarker) {
			t.Errorf("GET /%s (%s) renders %q", accountDeletionPage, lang, unfilledMarker)
		}
		if !strings.Contains(body, `href="/profile#account-delete"`) {
			t.Errorf("GET /%s (%s): missing the sign-in-and-delete link", accountDeletionPage, lang)
		}
		if strings.Contains(body, "static-notice") {
			t.Errorf("GET /%s (%s): deleted notice shown without ?deleted=1", accountDeletionPage, lang)
		}
		for _, f := range fragments {
			if !strings.Contains(body, f) {
				t.Errorf("GET /%s (%s) missing %q", accountDeletionPage, lang, f)
			}
		}
	}
}

func TestAccountDeletionPageShowsDeletedNotice(t *testing.T) {
	mux := newTestMux(t)
	req := httptest.NewRequest(http.MethodGet, "/"+accountDeletionPage+"?deleted=1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `class="static-notice"`) {
		t.Error("deleted notice missing")
	}
}

func TestAccountDeletionLinkedFromFooterAndPrivacy(t *testing.T) {
	mux := newTestMux(t)
	for _, lang := range staticPageLangs {
		body := getStaticPage(t, mux, "privacy", lang)
		if n := strings.Count(body, `href="/account-deletion"`); n < 2 {
			t.Errorf("GET /privacy (%s): want links to /account-deletion in footer and section 7, got %d", lang, n)
		}
	}
}

func TestAccountDeletionInSitemapAndNotBlockedByRobots(t *testing.T) {
	set := buildSitemap("https://cozy.kg", nil, nil)
	found := false
	for _, u := range set.URLs {
		if u.Loc == "https://cozy.kg/account-deletion" {
			found = true
		}
	}
	if !found {
		t.Error("sitemap missing /account-deletion")
	}
	for _, p := range robotsDisallow {
		if strings.HasPrefix("/"+accountDeletionPage, p) {
			t.Errorf("robots.txt Disallow %q blocks the public /%s page", p, accountDeletionPage)
		}
	}
}

func TestAccountDeleteGuestRedirectsToLogin(t *testing.T) {
	fake := &fakeDeleter{}
	w := postAccountDelete(t, &handlers{accounts: fake}, "", confirmedForm())

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/profile" {
		t.Fatalf("got %d → %q, want 303 → /profile", w.Code, w.Header().Get("Location"))
	}
	if len(fake.calls) != 0 {
		t.Errorf("DeleteCustomer called for a guest: %v", fake.calls)
	}
}

func TestAccountDeleteGuestViaRouterRedirectsToLogin(t *testing.T) {
	mux := newTestMux(t)
	req := httptest.NewRequest(http.MethodPost, "/account/delete", strings.NewReader(confirmedForm().Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/profile" {
		t.Fatalf("got %d → %q, want 303 → /profile", w.Code, w.Header().Get("Location"))
	}
}

func TestAccountDeleteDeletesAndLogsOut(t *testing.T) {
	fake := &fakeDeleter{}
	w := postAccountDelete(t, &handlers{accounts: fake}, "cust-1", confirmedForm())

	if len(fake.calls) != 1 || fake.calls[0] != "cust-1" {
		t.Fatalf("DeleteCustomer calls = %v, want [cust-1]", fake.calls)
	}
	if !sessionCookieCleared(w) {
		t.Errorf("session cookie not cleared: %v", w.Header().Values("Set-Cookie"))
	}
	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || loc != "/account-deletion?deleted=1" {
		t.Errorf("got %d → %q, want 303 → /account-deletion?deleted=1", w.Code, loc)
	}
}

func TestAccountDeleteHTMXUsesHXRedirect(t *testing.T) {
	fake := &fakeDeleter{}
	req := httptest.NewRequest(http.MethodPost, "/account/delete", strings.NewReader(confirmedForm().Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req = req.WithContext(context.WithValue(req.Context(), customerIDKey, "cust-1"))
	w := httptest.NewRecorder()
	if err := (&handlers{accounts: fake}).accountDelete(w, req); err != nil {
		t.Fatal(err)
	}
	if got := w.Header().Get("HX-Redirect"); got != "/account-deletion?deleted=1" {
		t.Errorf("HX-Redirect = %q", got)
	}
}

func TestAccountDeleteRequiresConfirmation(t *testing.T) {
	fake := &fakeDeleter{}
	w := postAccountDelete(t, &handlers{accounts: fake}, "cust-1", url.Values{})

	if len(fake.calls) != 0 {
		t.Fatalf("DeleteCustomer called without confirmation")
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/profile?delete="+deleteStatusUnconfirmed) {
		t.Errorf("Location = %q", loc)
	}
	if sessionCookieCleared(w) {
		t.Error("session cleared although nothing was deleted")
	}
}

func TestAccountDeleteActiveOrdersShowsReason(t *testing.T) {
	fake := &fakeDeleter{err: apperr.Conflict(codeHasActiveOrders, "есть незавершённые заказы")}
	w := postAccountDelete(t, &handlers{accounts: fake}, "cust-1", confirmedForm())

	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/profile?delete="+deleteStatusActiveOrders) {
		t.Fatalf("got %d → %q, want 303 → /profile?delete=active_orders", w.Code, loc)
	}
	if sessionCookieCleared(w) {
		t.Error("session cleared although the account was kept")
	}
}

func TestAccountDeleteAlreadyGoneStillLogsOut(t *testing.T) {
	fake := &fakeDeleter{err: apperr.NotFound(codeCustomerNotFound, "покупатель не найден")}
	w := postAccountDelete(t, &handlers{accounts: fake}, "cust-1", confirmedForm())
	if !sessionCookieCleared(w) || w.Header().Get("Location") != "/account-deletion?deleted=1" {
		t.Errorf("stale session should just be cleared: %d → %q", w.Code, w.Header().Get("Location"))
	}
}

func TestAccountDeleteUnexpectedErrorIsReturned(t *testing.T) {
	boom := errors.New("db down")
	req := httptest.NewRequest(http.MethodPost, "/account/delete", strings.NewReader(confirmedForm().Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), customerIDKey, "cust-1"))
	w := httptest.NewRecorder()
	err := (&handlers{accounts: &fakeDeleter{err: boom}}).accountDelete(w, req)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if sessionCookieCleared(w) {
		t.Error("session cleared on failure")
	}
}

func TestProfileRendersDeleteSectionAndReason(t *testing.T) {
	h := newTestHandlers(t)
	for _, lang := range staticPageLangs {
		reason := h.deleteStatusText(lang, deleteStatusActiveOrders)
		if reason == "" || reason == "account_delete.has_active_orders" {
			t.Fatalf("%s: has_active_orders text not translated: %q", lang, reason)
		}
		w := httptest.NewRecorder()
		data := PageData{Lang: lang, Screen: "profile", Authed: true, Data: ProfileData{DeleteError: reason}}
		if err := h.render.Render(w, "profile", data); err != nil {
			t.Fatalf("%s: Render: %v", lang, err)
		}
		body := w.Body.String()
		for _, want := range []string{`action="/account/delete"`, `name="confirm"`, `id="account-delete"`, reason} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: profile missing %q", lang, want)
			}
		}
	}
	if got := h.deleteStatusText(i18n.LangRU, "bogus"); got != "" {
		t.Errorf("unknown status text = %q, want empty", got)
	}
}
