package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// fakeStockUpserter is an in-memory stockSetter for tests, recording the
// last call so a test can assert whether the handler ever reached it.
type fakeStockUpserter struct {
	called                     bool
	lastVariantID, lastPointID string
	lastQuantity               int
	lastExpected               *int
	err                        error
}

func (f *fakeStockUpserter) Set(_ context.Context, variantID, pointID string, quantity int, expected *int) (*catalog.StockEntry, error) {
	f.called = true
	f.lastVariantID, f.lastPointID, f.lastQuantity, f.lastExpected = variantID, pointID, quantity, expected
	if f.err != nil {
		return nil, f.err
	}
	return &catalog.StockEntry{VariantID: variantID, PointID: pointID, Quantity: quantity}, nil
}

func newStockRequest(t *testing.T, st *staff.Staff, variantID, pointID, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, "/admin/api/stock/"+variantID+"/"+pointID, strings.NewReader(body))
	r.SetPathValue("variantId", variantID)
	r.SetPathValue("pointId", pointID)
	return r.WithContext(staff.NewContextWithStaff(r.Context(), st))
}

func pointStrPtr(s string) *string { return &s }

// This is the one RBAC nuance in Task D: RequireRole already lets
// owner/manager/point_staff all through to this handler for the stock
// route, but a point_staff member may only set stock at their own point.

func TestUpdateStockHandlerPointStaffOwnPointAllowed(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RolePointStaff, PointID: pointStrPtr("point-1")}
	fake := &fakeStockUpserter{}
	handler := apperr.Wrap(updateStockHandler(fake))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newStockRequest(t, st, "variant-1", "point-1", `{"quantity": 5}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !fake.called {
		t.Fatal("expected Upsert to be called for a point_staff acting on their own point")
	}
	if fake.lastVariantID != "variant-1" || fake.lastPointID != "point-1" || fake.lastQuantity != 5 {
		t.Fatalf("Upsert called with unexpected args: %+v", fake)
	}
}

func TestUpdateStockHandlerPointStaffOtherPointForbidden(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RolePointStaff, PointID: pointStrPtr("point-1")}
	fake := &fakeStockUpserter{}
	handler := apperr.Wrap(updateStockHandler(fake))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newStockRequest(t, st, "variant-1", "point-2", `{"quantity": 5}`))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if fake.called {
		t.Fatal("expected Upsert NOT to be called for a point_staff acting on another point")
	}
}

func TestUpdateStockHandlerOwnerCanSetAnyPoint(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RoleOwner}
	fake := &fakeStockUpserter{}
	handler := apperr.Wrap(updateStockHandler(fake))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newStockRequest(t, st, "variant-1", "point-99", `{"quantity": 3}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !fake.called {
		t.Fatal("expected Upsert to be called for an owner acting on any point")
	}
}

func TestUpdateStockHandlerManagerCanSetAnyPoint(t *testing.T) {
	st := &staff.Staff{ID: "s1", Role: staff.RoleManager}
	fake := &fakeStockUpserter{}
	handler := apperr.Wrap(updateStockHandler(fake))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newStockRequest(t, st, "variant-1", "point-99", `{"quantity": 3}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !fake.called {
		t.Fatal("expected Upsert to be called for a manager acting on any point")
	}
}

func TestUpdateStockHandlerPointStaffWithNoPointForbidden(t *testing.T) {
	// A point_staff record should always have a PointID (per the staff_role
	// enum semantics in §5 of the ТЗ), but the handler must not panic or
	// fail open if one somehow doesn't.
	st := &staff.Staff{ID: "s1", Role: staff.RolePointStaff, PointID: nil}
	fake := &fakeStockUpserter{}
	handler := apperr.Wrap(updateStockHandler(fake))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newStockRequest(t, st, "variant-1", "point-1", `{"quantity": 5}`))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if fake.called {
		t.Fatal("expected Upsert NOT to be called")
	}
}
