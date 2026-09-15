package catalog

import (
	"context"
	"testing"
)

func TestVariantCreateValidatesRequiredFields(t *testing.T) {
	repo := NewVariantRepo(nil)
	negative := -1.0

	cases := []struct {
		name string
		in   VariantInput
	}{
		{"missing size", VariantInput{Color: "black"}},
		{"missing color", VariantInput{Size: "42"}},
		{"negative price_override", VariantInput{Size: "42", Color: "black", PriceOverride: &negative}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := repo.Create(context.Background(), "product-1", c.in)
			assertAppErrStatus(t, err, 400)
		})
	}
}

func TestVariantUpdateValidatesRequiredFields(t *testing.T) {
	repo := NewVariantRepo(nil)

	_, err := repo.Update(context.Background(), "variant-1", VariantInput{Size: "42"})

	assertAppErrStatus(t, err, 400)
}
