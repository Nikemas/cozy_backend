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
