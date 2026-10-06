package catalog

import (
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// A 0 in the price_override column must not become price_override = 0 —
// that sells the variant for free and blocks the admin product form.
// 0 there means "no override", like an empty cell.
func TestImportZeroPriceOverrideIsNoOverride(t *testing.T) {
	s := newMemStore()
	csv := "model_code,name_ru,category,price,price_override,size,color,sku\n" +
		"Z-1,Кеды Zero,sneakers,4990,0,40,white,Z-1-40\n" +
		"Z-1,Кеды Zero,sneakers,4990,5200,42,white,Z-1-42\n"
	runCSV(t, s, csv, ImportOptions{})

	v, ok := s.variantBySKU("Z-1-40")
	if !ok {
		t.Fatalf("variant Z-1-40 not imported (variants %+v)", s.st.variants)
	}
	if v.PriceOverride != nil {
		t.Errorf("Z-1-40 override = %v, want nil (0 = no override)", *v.PriceOverride)
	}
	if v, _ := s.variantBySKU("Z-1-42"); v.PriceOverride == nil || *v.PriceOverride != 5200 {
		t.Errorf("Z-1-42 override = %v, want 5200", v.PriceOverride)
	}
}

// A 0 in the price column is a row error — whether it's the row that
// would set the model's base price or a later size row — never a product
// sold for 0. The rest of the model is skipped, like any other row error.
func TestImportZeroPriceIsRowError(t *testing.T) {
	csv := "model_code,name_ru,category,price,size,color,sku\n" +
		"Z-1,Кеды Zero,sneakers,0,40,white,Z-1-40\n" +
		"Z-1,Кеды Zero,sneakers,4990,41,white,Z-1-41\n" +
		"Z-2,Кеды Two,sneakers,4990,40,white,Z-2-40\n" +
		"Z-2,Кеды Two,sneakers,0.004,41,white,Z-2-41\n" +
		"Z-3,Кеды Ok,sneakers,4990,40,white,Z-3-40\n"
	cases := []struct{ lang, want0, wantRounded string }{
		{i18n.LangRU, `цена должна быть больше нуля: "0"`, `цена должна быть больше нуля: "0.004"`},
		{i18n.LangKY, `баасы нөлдөн чоң болушу керек: "0"`, `баасы нөлдөн чоң болушу керек: "0.004"`},
	}
	for _, c := range cases {
		t.Run(c.lang, func(t *testing.T) {
			s := newMemStore()
			res := runCSV(t, s, csv, ImportOptions{Lang: c.lang})
			if r := rowStatus(t, res, 2); r.Status != RowStatusError || r.Message != c.want0 {
				t.Errorf("row 2 = %+v, want error %q", r, c.want0)
			}
			if r := rowStatus(t, res, 3); r.Status != RowStatusSkipped {
				t.Errorf("row 3 = %+v, want skipped", r)
			}
			if r := rowStatus(t, res, 5); r.Status != RowStatusError || r.Message != c.wantRounded {
				t.Errorf("row 5 = %+v, want error %q", r, c.wantRounded)
			}
			for _, name := range []string{"Кеды Zero", "Кеды Two"} {
				if _, ok := s.productByName(name); ok {
					t.Errorf("%s imported despite a 0 price", name)
				}
			}
			if p, ok := s.productByName("Кеды Ok"); !ok || p.BasePrice != 4990 {
				t.Errorf("valid model = %+v ok=%v", p, ok)
			}
		})
	}
}
