//go:build integration

package integration

import (
	"testing"

	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// TestPromoPushIsOptIn: a new customer is not subscribed to promo pushes
// and has not been asked (App Store guideline 4.5.4); any explicit
// promo_push answer stamps promo_push_asked_at, other profile edits don't.
func TestPromoPushIsOptIn(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	repo := storefront.NewCustomerRepo(testDB)

	c, err := repo.GetOrCreateByPhone(ctx, uniquePhone())
	if err != nil {
		t.Fatal(err)
	}
	if c.PromoPush || c.PromoPushAsked {
		t.Fatalf("new customer = %+v; want promo_push=false, not asked", c)
	}

	name := "Тест"
	c, err = repo.UpdateProfile(ctx, c.ID, storefront.ProfileUpdate{Name: &name})
	if err != nil || c.PromoPushAsked {
		t.Fatalf("after name-only update = %+v, %v; want still not asked", c, err)
	}

	off := false
	c, err = repo.UpdateProfile(ctx, c.ID, storefront.ProfileUpdate{PromoPush: &off})
	if err != nil || c.PromoPush || !c.PromoPushAsked {
		t.Fatalf("after declining = %+v, %v; want promo_push=false, asked", c, err)
	}

	on := true
	c, err = repo.UpdateProfile(ctx, c.ID, storefront.ProfileUpdate{PromoPush: &on})
	if err != nil || !c.PromoPush || !c.PromoPushAsked {
		t.Fatalf("after opting in = %+v, %v; want promo_push=true, asked", c, err)
	}
}
