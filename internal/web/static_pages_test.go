package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// unfilledMarker is the editorial placeholder the page drafts used for
// data still owed by the client; it must never reach a public page.
const unfilledMarker = "[ЗАПОЛНИТЬ"

var staticPageLangs = []string{i18n.LangRU, i18n.LangKY}

func getStaticPage(t *testing.T, mux *http.ServeMux, name, lang string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/"+name, nil)
	req.AddCookie(&http.Cookie{Name: langCookieName, Value: lang})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /%s (%s): status = %d, want 200", name, lang, w.Code)
	}
	return w.Body.String()
}

func filledContactsConfig() *config.Config {
	return &config.Config{
		JWTSecret:          "test-secret-test-secret-test-secret",
		PublicBaseURL:      "https://cozy.test",
		AppStoreURLIOS:     "https://apps.apple.com/app/cozy/id1",
		AppStoreURLAndroid: "https://play.google.com/store/apps/details?id=kg.cozy",
		Contacts: config.Contacts{
			Phone:       "+996 555 123 456",
			WhatsApp:    "+996 700 000 001",
			Telegram:    "cozy_kg",
			Email:       "help@cozy.test",
			Hours:       "10:00–20:00",
			BankDetails: "Test Bank, BIC 124001",
		},
	}
}

func TestStaticPagesHaveNoUnfilledPlaceholders(t *testing.T) {
	configs := map[string]*config.Config{
		"empty contacts":  {JWTSecret: "test-secret-test-secret-test-secret", PublicBaseURL: "https://cozy.test"},
		"filled contacts": filledContactsConfig(),
	}
	for label, cfg := range configs {
		mux := newTestMuxWithConfig(t, cfg)
		for _, name := range staticPages {
			for _, lang := range staticPageLangs {
				body := getStaticPage(t, mux, name, lang)
				if strings.Contains(body, unfilledMarker) {
					t.Errorf("%s: GET /%s (%s) renders %q", label, name, lang, unfilledMarker)
				}
				if strings.Contains(body, `class="fill"`) {
					t.Errorf("%s: GET /%s (%s) still has a fill-in highlight", label, name, lang)
				}
			}
		}
	}
}

func TestStaticPagesOmitContactsWhenUnset(t *testing.T) {
	mux := newTestMux(t)
	for _, name := range []string{"contacts", "privacy", "terms"} {
		for _, lang := range staticPageLangs {
			body := getStaticPage(t, mux, name, lang)
			for _, unwanted := range []string{"tel:", "mailto:", "wa.me", "t.me/", "static-contacts__item", "Телефон:"} {
				if strings.Contains(body, unwanted) {
					t.Errorf("GET /%s (%s) with no contacts configured contains %q", name, lang, unwanted)
				}
			}
		}
	}
	for _, lang := range staticPageLangs {
		if body := getStaticPage(t, mux, "contacts", lang); strings.Contains(body, `class="static-contacts"`) {
			t.Errorf("GET /contacts (%s): empty contacts block should not render", lang)
		}
		if body := getStaticPage(t, mux, "about", lang); strings.Contains(body, "App Store") || strings.Contains(body, "Google Play") {
			t.Errorf("GET /about (%s): store links should not render without APP_STORE_URL_*", lang)
		}
	}
}

func TestStaticPagesRenderConfiguredContacts(t *testing.T) {
	mux := newTestMuxWithConfig(t, filledContactsConfig())
	want := map[string][]string{
		"contacts": {`href="tel:&#43;996555123456"`, "&#43;996 555 123 456", `href="https://wa.me/996700000001"`, `href="https://t.me/cozy_kg"`, "@cozy_kg", `href="mailto:help@cozy.test"`, "10:00–20:00"},
		"privacy":  {`href="tel:&#43;996555123456"`, `href="mailto:help@cozy.test"`},
		"terms":    {`href="tel:&#43;996555123456"`, `href="mailto:help@cozy.test"`, "Test Bank, BIC 124001"},
		"about":    {`href="https://apps.apple.com/app/cozy/id1"`, "Google Play"},
	}
	for name, fragments := range want {
		for _, lang := range staticPageLangs {
			body := getStaticPage(t, mux, name, lang)
			for _, f := range fragments {
				if !strings.Contains(body, f) {
					t.Errorf("GET /%s (%s) missing %q", name, lang, f)
				}
			}
		}
	}
}

func TestContactsPageRendersOnlyConfiguredPhone(t *testing.T) {
	cfg := &config.Config{
		JWTSecret:     "test-secret-test-secret-test-secret",
		PublicBaseURL: "https://cozy.test",
		Contacts:      config.Contacts{Phone: "+996 555 123 456"},
	}
	mux := newTestMuxWithConfig(t, cfg)
	for _, lang := range staticPageLangs {
		body := getStaticPage(t, mux, "contacts", lang)
		if !strings.Contains(body, "ti-phone") || !strings.Contains(body, "&#43;996 555 123 456") {
			t.Errorf("GET /contacts (%s): phone block missing", lang)
		}
		for _, unwanted := range []string{"ti-brand-whatsapp", "ti-brand-telegram", "ti-mail", "ti-clock"} {
			if strings.Contains(body, unwanted) {
				t.Errorf("GET /contacts (%s): unconfigured channel %q rendered", lang, unwanted)
			}
		}
	}
}
