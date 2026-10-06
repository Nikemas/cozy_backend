//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/points"
)

// A point that still holds goods must not be deletable: stock.point_id is
// ON DELETE CASCADE, so a plain DELETE would silently wipe its stock rows.
func TestDeletePointWithStockIsRefused(t *testing.T) {
	f := newFixture(t, 3, 0)
	repo := points.NewPointsRepo(testDB)

	err := repo.Delete(ctxT(t), f.PointA)

	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.Status != 409 {
		t.Fatalf("Delete(point with stock) err = %v, want 409 conflict", err)
	}
	var qty int
	if err := testDB.QueryRowContext(ctxT(t),
		`SELECT COALESCE(SUM(quantity), 0) FROM stock WHERE point_id = $1`, f.PointA).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != 3 {
		t.Errorf("stock after refused delete = %d, want 3", qty)
	}
}

// Zero-quantity stock rows are just empty cells; they don't block deleting
// a point that has nothing on its shelves.
func TestDeletePointWithOnlyEmptyStockSucceeds(t *testing.T) {
	f := newFixture(t, 0, 0)
	repo := points.NewPointsRepo(testDB)

	if err := repo.Delete(ctxT(t), f.PointA); err != nil {
		t.Fatalf("Delete(point with empty stock) err = %v, want nil", err)
	}
}
