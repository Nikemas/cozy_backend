package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// The import check runs in the staff member's language: row messages
// come back in it (ImportOptions.Lang) and the swapped-in fragment is
// rendered in it.
func TestImportCheckUsesAdminLanguage(t *testing.T) {
	// Arrange
	imp := &fakeImporter{result: cleanImportResult()}
	h := newImportHandlers(t, imp)
	r := importRequest(t, importCheckPath, "A", "", "", true)
	r = r.WithContext(contextWithLang(r.Context(), i18n.LangKY))
	w := httptest.NewRecorder()

	// Act
	h.productImportCheck(&langWriter{ResponseWriter: w, lang: i18n.LangKY}, r)

	// Assert
	if len(imp.calls) != 1 || imp.calls[0].Lang != i18n.LangKY {
		t.Fatalf("calls = %+v, want Lang=ky", imp.calls)
	}
	if body := w.Body.String(); !strings.Contains(body, kyTr.T("admin.import.run")) || !strings.Contains(body, kyTr.T("admin.import.dry_ok")) {
		t.Errorf("fragment not in Kyrgyz:\n%s", body)
	}
}
