package web

import (
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// TestCheckoutCommentFieldLayout: the order comment is a labelled,
// stacked field (label above a full-width textarea) living in the main
// checkout column next to the delivery/payment options — not a bare
// inline <label> sitting as its own flex item beside the summary, which
// rendered the label and a tiny textarea on one line.
func TestCheckoutCommentFieldLayout(t *testing.T) {
	rr := newTestRenderer(t)
	data := PageData{Lang: i18n.LangRU, Screen: "checkout", Authed: true, Data: CheckoutPageData{
		Points:        []PointView{{ID: "p1", Name: "Cozy", Address: "ул. Чуй 1"}},
		ItemsTotal:    5000,
		ItemCount:     1,
		CommentMaxLen: 500,
	}}

	w := httptest.NewRecorder()
	if err := rr.Render(w, "checkout", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()

	if !strings.Contains(body, `<label class="field-label" for="checkout-comment">`) {
		t.Errorf("comment label is not a field-label bound to the textarea")
	}
	if !regexp.MustCompile(`<textarea id="checkout-comment" name="comment" rows="3"`).MatchString(body) {
		t.Errorf("comment textarea missing id/rows")
	}
	main := strings.Index(body, `class="checkout-main"`)
	comment := strings.Index(body, `class="checkout-comment"`)
	summary := strings.Index(body, `class="checkout-summary"`)
	if main < 0 || comment < main || summary < comment {
		t.Errorf("want checkout-main > checkout-comment before checkout-summary; got main=%d comment=%d summary=%d", main, comment, summary)
	}
}

// TestCheckoutCommentCSS pins the stacked, full-width styling in site.css.
func TestCheckoutCommentCSS(t *testing.T) {
	css, err := os.ReadFile(repoRoot(t) + "/web/static/css/site.css")
	if err != nil {
		t.Fatal(err)
	}
	s := string(css)
	for _, want := range []string{
		".checkout-main {",
		".checkout-comment {",
		".checkout-comment textarea {",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("site.css missing %q", want)
		}
	}
	start := strings.Index(s, ".checkout-comment textarea {")
	if start < 0 {
		return
	}
	rule := s[start:]
	rule = rule[:strings.Index(rule, "}")]
	if !strings.Contains(rule, "width: 100%") {
		t.Errorf(".checkout-comment textarea should be full width; rule: %s", rule)
	}
}
