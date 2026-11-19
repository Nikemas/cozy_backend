//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/web"
)

// Discount display: products.old_price is shown struck through next to
// the current price only where it is above the price shown, is exposed by
// the public API, and the DB rejects a non-positive value.
func TestProductOldPriceStorefrontAndAPI(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	a := app(t)
	f := newFixture(t, 2, 2) // base 2500; variant 41 has price_override 3000
	repo := catalog.NewProductRepo(testDB)

	p, err := repo.GetByID(ctx, f.ProductID)
	if err != nil {
		t.Fatal(err)
	}
	if p.OldPrice != nil {
		t.Fatalf("new product OldPrice = %v, want nil", *p.OldPrice)
	}

	old := 2800.0
	in := catalog.ProductInput{CategoryID: p.CategoryID, NameRu: p.NameRu, NameKy: p.NameKy, Brand: p.Brand,
		BasePrice: p.BasePrice, OldPrice: &old, IsActive: true}
	if _, err := repo.Update(ctx, f.ProductID, in); err != nil {
		t.Fatalf("Update with old price: %v", err)
	}
	if p, err = repo.GetByID(ctx, f.ProductID); err != nil || p.OldPrice == nil || *p.OldPrice != old {
		t.Fatalf("GetByID OldPrice = %v (err %v), want %v", p.OldPrice, err, old)
	}

	page := web.ProductPath(f.ProductID, p.NameRu)
	w := a.get(t, page+"?size=40&color=black")
	wantStatus(t, w, http.StatusOK)
	wantBody(t, w, `<s class="price-old">`, "price--sale")

	// Variant 41 sells at 3000 — above the old price, so no discount shown.
	w = a.get(t, page+"?size=41&color=black")
	wantStatus(t, w, http.StatusOK)
	if strings.Contains(w.Body.String(), `<s class="price-old">`) {
		t.Error("variant priced above old_price must not show a struck-through price")
	}

	w = a.get(t, "/api/v1/products/"+f.ProductID)
	wantStatus(t, w, http.StatusOK)
	var body struct {
		OldPrice *float64 `json:"old_price"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.OldPrice == nil || *body.OldPrice != old {
		t.Fatalf("API old_price = %v (err %v), want %v", body.OldPrice, err, old)
	}

	// An import can raise base_price above old_price; later full-row
	// writes (the activate toggle) must keep working and keep old_price.
	below := 2000.0
	in.OldPrice = &below
	in.IsActive = false
	if _, err := repo.Update(ctx, f.ProductID, in); err != nil {
		t.Errorf("Update with a stale old price must succeed: %v", err)
	}
	if _, err := testDB.ExecContext(ctx, `UPDATE products SET old_price = 0 WHERE id = $1`, f.ProductID); err == nil {
		t.Error("DB accepted old_price = 0")
	}
}
