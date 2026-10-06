package catalog

import "testing"

// A 0 in a variant's price cell (explicit price_override, or a size row's
// own price differing from the model's) must not become price_override = 0
// — that sells the variant for free and blocks the admin product form.
// 0 means "no override", like an empty cell.
func TestImportZeroVariantPriceIsNoOverride(t *testing.T) {
	s := newMemStore()
	csv := "model_code,name_ru,category,price,price_override,size,color,sku\n" +
		"Z-1,Кеды Zero,sneakers,4990,0,40,white,Z-1-40\n" +
		"Z-1,Кеды Zero,sneakers,0,,41,white,Z-1-41\n" +
		"Z-1,Кеды Zero,sneakers,4990,5200,42,white,Z-1-42\n"
	runCSV(t, s, csv, ImportOptions{})

	for _, sku := range []string{"Z-1-40", "Z-1-41"} {
		v, ok := s.variantBySKU(sku)
		if !ok {
			t.Fatalf("variant %s not imported (variants %+v)", sku, s.st.variants)
		}
		if v.PriceOverride != nil {
			t.Errorf("%s override = %v, want nil (0 = no override)", sku, *v.PriceOverride)
		}
	}
	if v, _ := s.variantBySKU("Z-1-42"); v.PriceOverride == nil || *v.PriceOverride != 5200 {
		t.Errorf("Z-1-42 override = %v, want 5200", v.PriceOverride)
	}
}
