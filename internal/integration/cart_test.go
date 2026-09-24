//go:build integration

package integration

import (
	"testing"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

func TestCartRepo(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 10, 10)
	cart := orders.NewCartRepo(testDB)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	qtys := func() map[string]int {
		t.Helper()
		items, err := cart.List(ctx, f.CustomerID)
		must(err)
		out := map[string]int{}
		for _, it := range items {
			out[it.VariantID] = it.Qty
		}
		return out
	}

	must(cart.Add(ctx, f.CustomerID, f.VariantA, 1))
	must(cart.Add(ctx, f.CustomerID, f.VariantA, 2)) // upsert adds up
	must(cart.Add(ctx, f.CustomerID, f.VariantB, 1))
	if got := qtys(); len(got) != 2 || got[f.VariantA] != 3 || got[f.VariantB] != 1 {
		t.Fatalf("cart after adds = %v, want A:3 B:1", got)
	}

	must(cart.UpdateQty(ctx, f.CustomerID, f.VariantB, 4))
	if got := qtys(); got[f.VariantB] != 4 {
		t.Errorf("B after UpdateQty = %d, want 4", got[f.VariantB])
	}

	must(cart.UpdateQty(ctx, f.CustomerID, f.VariantB, 0)) // 0 removes the line
	must(cart.Remove(ctx, f.CustomerID, f.VariantA))
	must(cart.Remove(ctx, f.CustomerID, f.VariantA)) // removing twice is a no-op
	if got := qtys(); len(got) != 0 {
		t.Errorf("cart after removals = %v, want empty", got)
	}

	// Another customer's cart is separate.
	other := newCustomer(t)
	must(cart.Add(ctx, other, f.VariantA, 1))
	if got := qtys(); len(got) != 0 {
		t.Errorf("customer sees another customer's cart: %v", got)
	}
}
