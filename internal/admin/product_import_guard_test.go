package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

// importEndpoints are the two handlers that read and run an upload.
var importEndpoints = map[string]struct {
	path string
	fn   func(*handlers, http.ResponseWriter, *http.Request)
}{
	"check": {importCheckPath, (*handlers).productImportCheck},
	"apply": {importApplyPath, (*handlers).productImportApply},
}

func TestImportRefusesRequestWithoutStaffID(t *testing.T) {
	noID := &staff.Staff{Role: staff.RoleManager, IsActive: true}
	contexts := map[string]context.Context{
		"no staff":       context.Background(),
		"empty staff ID": staff.NewContextWithStaff(context.Background(), noID),
	}
	for epName, ep := range importEndpoints {
		for ctxName, ctx := range contexts {
			t.Run(epName+"/"+ctxName, func(t *testing.T) {
				// Arrange
				imp := &fakeImporter{result: cleanImportResult()}
				h := newImportHandlers(t, imp)
				lim := &fakeImportLimiter{n: 100}
				h.importLimiter = lim
				r := importRequest(t, ep.path, "A", "pt1", "", true)
				r = r.WithContext(ctx)
				w := httptest.NewRecorder()

				// Act
				ep.fn(h, w, r)

				// Assert
				if w.Code != http.StatusForbidden {
					t.Errorf("status = %d, want 403", w.Code)
				}
				if len(lim.keys) != 0 {
					t.Errorf("limiter asked about %v: an empty ID must not share one bucket", lim.keys)
				}
				if len(imp.calls) != 0 {
					t.Errorf("importer called without a staff ID: %+v", imp.calls)
				}
				if !strings.Contains(w.Body.String(), ruTr.T("admin.err.generic")) {
					t.Errorf("want the generic error:\n%s", w.Body.String())
				}
			})
		}
	}
}
