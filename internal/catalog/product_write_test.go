package catalog

import (
	"context"
	"testing"
)

func TestProductCreateValidatesRequiredFields(t *testing.T) {
	repo := NewProductRepo(nil)

	cases := []struct {
		name string
		in   ProductInput
	}{
		{"missing category_id", ProductInput{NameRu: "кроссовки", NameKy: "кроссовки", BasePrice: 100}},
		{"missing name_ru", ProductInput{CategoryID: "c1", NameKy: "кроссовки", BasePrice: 100}},
		{"missing name_ky", ProductInput{CategoryID: "c1", NameRu: "кроссовки", BasePrice: 100}},
		{"negative base_price", ProductInput{CategoryID: "c1", NameRu: "кроссовки", NameKy: "кроссовки", BasePrice: -1}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := repo.Create(context.Background(), c.in)
			assertAppErrStatus(t, err, 400)
		})
	}
}

func TestProductUpdateValidatesRequiredFields(t *testing.T) {
	repo := NewProductRepo(nil)

	_, err := repo.Update(context.Background(), "p1", ProductInput{NameRu: "кроссовки"})

	assertAppErrStatus(t, err, 400)
}

func TestProductInputValidateAllowsZeroBasePrice(t *testing.T) {
	// Only a negative base_price should be rejected — zero is valid input
	// (e.g. a promotional giveaway item), unlike the negative case above.
	in := ProductInput{CategoryID: "c1", NameRu: "кроссовки", NameKy: "кроссовки", BasePrice: 0}
	if err := in.validate(); err != nil {
		t.Fatalf("expected zero base_price to be valid, got error: %v", err)
	}
}

// --- buildAdminListConditions (internal/admin's ListForAdmin query building) ---

func TestBuildAdminListConditionsEmpty(t *testing.T) {
	conditions, args := buildAdminListConditions(AdminListFilter{})
	if len(conditions) != 0 {
		t.Fatalf("expected no conditions, got %v", conditions)
	}
	if len(args) != 0 {
		t.Fatalf("expected no args, got %v", args)
	}
}

func TestBuildAdminListConditionsCategoryIDs(t *testing.T) {
	conditions, args := buildAdminListConditions(AdminListFilter{CategoryIDs: []string{"c1", "c2"}})
	if len(conditions) != 1 || conditions[0] != "category_id = ANY($1)" {
		t.Fatalf("conditions = %v, want [category_id = ANY($1)]", conditions)
	}
	if len(args) != 1 {
		t.Fatalf("args = %v, want 1 arg", args)
	}
	ids, ok := args[0].([]string)
	if !ok || len(ids) != 2 {
		t.Fatalf("args[0] = %v, want []string{c1,c2}", args[0])
	}
}

func TestBuildAdminListConditionsQuery(t *testing.T) {
	conditions, args := buildAdminListConditions(AdminListFilter{Query: "nike"})
	if len(conditions) != 1 || conditions[0] != "(name_ru ILIKE $1 OR name_ky ILIKE $1)" {
		t.Fatalf("conditions = %v, want name_ru/name_ky ILIKE on $1", conditions)
	}
	if len(args) != 1 || args[0] != "%nike%" {
		t.Fatalf("args = %v, want [%%nike%%]", args)
	}
}

func TestBuildAdminListConditionsCategoryAndQueryUseSeparatePlaceholders(t *testing.T) {
	conditions, args := buildAdminListConditions(AdminListFilter{CategoryIDs: []string{"c1"}, Query: "nike"})
	if len(conditions) != 2 {
		t.Fatalf("conditions = %v, want 2 conditions", conditions)
	}
	if conditions[0] != "category_id = ANY($1)" {
		t.Errorf("conditions[0] = %q, want category_id = ANY($1)", conditions[0])
	}
	if conditions[1] != "(name_ru ILIKE $2 OR name_ky ILIKE $2)" {
		t.Errorf("conditions[1] = %q, want name_ru/name_ky ILIKE on $2", conditions[1])
	}
	if len(args) != 2 {
		t.Fatalf("args = %v, want 2 args", args)
	}
}
