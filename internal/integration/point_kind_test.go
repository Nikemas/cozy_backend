//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

func makeWarehouse(t *testing.T, pointID string) {
	t.Helper()
	if _, err := testDB.ExecContext(ctxT(t),
		`UPDATE points_of_sale SET kind = 'warehouse' WHERE id = $1`, pointID); err != nil {
		t.Fatal(err)
	}
}

// A warehouse only stores goods: customers never see it on the branches
// screen / map.
func TestWarehouseHiddenFromBranches(t *testing.T) {
	f := newFixture(t, 1, 0)
	makeWarehouse(t, f.PointB)

	list, err := storefront.NewBranchRepo(testDB).List(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}

	seenA := false
	for _, b := range list {
		if b.ID == f.PointB {
			t.Errorf("warehouse %s listed as a branch", f.PointB)
		}
		if b.ID == f.PointA {
			seenA = true
		}
	}
	if !seenA {
		t.Errorf("branch %s missing from branches list", f.PointA)
	}
}

// A warehouse can't be picked for self-pickup, even if it holds the goods.
func TestWarehouseRejectedAsPickupPoint(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 2, 0)
	makeWarehouse(t, f.PointA)
	svc, _ := newService()

	_, err := svc.CreateOrder(ctxT(t), f.CustomerID,
		[]orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 1}}, nil, strptr(f.PointA))

	wantAppErr(t, err, http.StatusNotFound)
}

// The admin repo round-trips the kind and keeps warehouses in its list.
func TestPointsRepoStoresKind(t *testing.T) {
	repo := points.NewPointsRepo(testDB)

	p, err := repo.Create(ctxT(t), points.PointInput{
		Name: "Склад IT", City: "Бишкек", Address: "ул. Складская, 5", Kind: points.KindWarehouse,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctxT(t), p.ID) })

	if p.Kind != points.KindWarehouse {
		t.Errorf("created Kind = %q, want warehouse", p.Kind)
	}
	got, err := repo.GetByID(ctxT(t), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != points.KindWarehouse {
		t.Errorf("stored Kind = %q, want warehouse", got.Kind)
	}
}
