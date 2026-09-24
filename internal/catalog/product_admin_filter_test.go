package catalog

import (
	"strings"
	"testing"
)

func TestBuildAdminListConditionsOutOfStockAtPoint(t *testing.T) {
	conditions, args := buildAdminListConditions(AdminListFilter{Query: "nike", OutOfStockAtPoint: "pt-1"})
	if len(conditions) != 2 || len(args) != 2 {
		t.Fatalf("conditions=%v args=%v, want 2 of each", conditions, args)
	}
	c := conditions[1]
	if !strings.HasPrefix(c, "NOT EXISTS (") || !strings.Contains(c, "s.point_id = $2") || !strings.Contains(c, "s.quantity > 0") {
		t.Errorf("condition = %q", c)
	}
	if args[1] != "pt-1" {
		t.Errorf("args[1] = %v, want pt-1", args[1])
	}
}
