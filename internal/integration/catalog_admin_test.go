//go:build integration

package integration

import (
	"testing"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

// The products list's "нет в наличии в точке" filter: a product counts as
// out of stock at a point when none of its variants has quantity > 0
// there (no stock row at all counts as 0).
func TestListForAdminOutOfStockAtPoint(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 0, 2) // A: 0 and B: 2 at PointA; nothing at PointB
	repo := catalog.NewProductRepo(testDB)

	count := func(pointID string) int {
		t.Helper()
		_, total, err := repo.ListForAdmin(ctx, catalog.AdminListFilter{CategoryIDs: []string{f.CategoryID}, OutOfStockAtPoint: pointID})
		if err != nil {
			t.Fatalf("ListForAdmin: %v", err)
		}
		return total
	}
	if got := count(f.PointA); got != 0 {
		t.Errorf("out of stock at A = %d, want 0 (variant B has 2)", got)
	}
	if got := count(f.PointB); got != 1 {
		t.Errorf("out of stock at B = %d, want 1 (no stock rows)", got)
	}
	if _, err := testDB.ExecContext(ctx, `UPDATE stock SET quantity = 0 WHERE variant_id = $1 AND point_id = $2`, f.VariantB, f.PointA); err != nil {
		t.Fatal(err)
	}
	if got := count(f.PointA); got != 1 {
		t.Errorf("out of stock at A after selling out = %d, want 1", got)
	}
}
