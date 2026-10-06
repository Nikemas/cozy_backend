package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// TestProfileAuthPhoneStepKeepsTypedValueOnError: when the phone step is
// re-rendered with a validation error, the visitor's input must stay in
// the field (escaped) instead of being wiped, so they can fix a typo.
func TestProfileAuthPhoneStepKeepsTypedValueOnError(t *testing.T) {
	rr := newTestRenderer(t)
	data := PageData{Lang: i18n.LangRU, Screen: "profile", Data: ProfileData{Phone: `12 "3`, Error: "bad phone"}}

	w := httptest.NewRecorder()
	if err := rr.RenderPartial(w, "profile", "_profile_auth", data); err != nil {
		t.Fatalf("RenderPartial: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, `value="12 &#34;3"`) {
		t.Errorf("phone input lost the typed value; body:\n%s", body)
	}
	if !strings.Contains(body, "bad phone") {
		t.Errorf("error message missing")
	}
}
