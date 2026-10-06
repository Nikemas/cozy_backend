package web

import (
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// TestPrivacyPageDisclosesDataFlows pins the third-party data flows the
// code actually has (App Store review: the policy must name every party
// that receives user data) in both languages.
func TestPrivacyPageDisclosesDataFlows(t *testing.T) {
	common := []string{
		"Firebase Cloud Messaging",
		"Firebase Crashlytics",
		"Telegram",
		"Google Maps",
		"Nikita",
		"Bakai",
		`href="/account-deletion"`,
		`id="promo-push"`,
	}
	perLang := map[string][]string{
		i18n.LangRU: {"Уведомления об акциях и скидках", "не связаны с вашим номером"},
		i18n.LangKY: {"Акциялар жана арзандатуулар тууралуу билдирмелер"},
	}
	mux := newTestMux(t)
	for _, lang := range staticPageLangs {
		body := getStaticPage(t, mux, "privacy", lang)
		for _, want := range append(append([]string{}, common...), perLang[lang]...) {
			if !strings.Contains(body, want) {
				t.Errorf("GET /privacy (%s) missing %q", lang, want)
			}
		}
		if strings.Contains(body, "native review") {
			t.Errorf("GET /privacy (%s): reviewer note leaked into the page", lang)
		}
	}
}
