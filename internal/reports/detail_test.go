package reports

import (
	"strings"
	"testing"
)

func TestTotalProductSales(t *testing.T) {
	got := TotalProductSales([]ProductSalesRow{
		{Name: "A", OrderCount: 2, ItemCount: 3, Revenue: 300.5, CancelledCount: 1},
		{Name: "B", OrderCount: 1, ItemCount: 1, Revenue: 99.5},
	})
	if got.ItemCount != 4 || got.Revenue != 400 || got.CancelledCount != 1 || got.OrderCount != 3 || got.Name != "" {
		t.Fatalf("total = %+v", got)
	}
	if z := TotalProductSales(nil); z != (ProductSalesRow{}) {
		t.Fatalf("empty total = %+v", z)
	}
}

func TestTotalVariantSales(t *testing.T) {
	got := TotalVariantSales([]VariantSalesRow{{ItemCount: 2, Revenue: 10}, {ItemCount: 1, Revenue: 5, CancelledCount: 2}})
	if got.ItemCount != 3 || got.Revenue != 15 || got.CancelledCount != 2 {
		t.Fatalf("total = %+v", got)
	}
}

func TestPointDisplayKey(t *testing.T) {
	names := map[string]string{"p1": "ЦУМ", "p2": ""}
	for in, want := range map[string]string{"": UnknownPointKey, "p1": "ЦУМ", "p2": "p2", "p3": "p3"} {
		if got := pointDisplayKey(in, names); got != want {
			t.Errorf("pointDisplayKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// The detail reports must count sales exactly like the summaries.
func TestDetailQueriesUseSaleCondition(t *testing.T) {
	q := productSalesQuery("WITH scope AS (SELECT 1 AS id)")
	if !strings.Contains(q, SaleConditionSQL) || !strings.Contains(q, "o.status = 'cancelled'") {
		t.Fatalf("query lacks the sale/cancel conditions:\n%s", q)
	}
}
