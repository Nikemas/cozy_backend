package web

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// TestFormFieldsHaveLabels guards the placeholder-only inputs the audit
// found: every login step and the address form must label their fields.
func TestFormFieldsHaveLabels(t *testing.T) {
	rr := newTestRenderer(t)

	for _, step := range []string{"", "otp", "name"} {
		w := httptest.NewRecorder()
		data := PageData{Lang: i18n.LangKY, Screen: "profile", Data: ProfileData{Step: step, Phone: "+996700000000"}}
		if err := rr.Render(w, "profile", data); err != nil {
			t.Fatalf("Render(step=%q): %v", step, err)
		}
		assertInputsLabelled(t, "profile step "+step, w.Body.String())
	}

	for _, form := range []*AddressFormData{{}, {ID: "a1", Label: "Дом", AddressText: "ул. Ленина 1"}} {
		w := httptest.NewRecorder()
		if err := rr.tmpl[i18n.LangRU]["addresses"].ExecuteTemplate(w, "_address_form", form); err != nil {
			t.Fatalf("_address_form: %v", err)
		}
		assertInputsLabelled(t, "address form "+form.ID, w.Body.String())
	}
}

var inputIDRe = regexp.MustCompile(`<(?:input|textarea)[^>]*\bid="([^"]+)"`)

func assertInputsLabelled(t *testing.T, name, body string) {
	t.Helper()
	ids := inputIDRe.FindAllStringSubmatch(body, -1)
	if len(ids) == 0 {
		t.Fatalf("%s: no identifiable inputs rendered", name)
	}
	for _, m := range ids {
		if !strings.Contains(body, `for="`+m[1]+`"`) {
			t.Errorf("%s: input #%s has no <label for>", name, m[1])
		}
	}
}

func TestLayoutHasSkipLinkAndPinnedHTMX(t *testing.T) {
	rr := newTestRenderer(t)
	w := httptest.NewRecorder()
	if err := rr.Render(w, "lang", PageData{Lang: i18n.LangRU, Screen: "lang"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="#main"`) || !strings.Contains(body, `id="main"`) {
		t.Error("layout should have a skip link to #main")
	}
	if !strings.Contains(body, `integrity="sha384-`) {
		t.Error("htmx script must carry an SRI integrity hash")
	}
	if strings.Count(body, `id="toast-slot"`) != 1 {
		t.Error("layout must render exactly one #toast-slot")
	}
}
