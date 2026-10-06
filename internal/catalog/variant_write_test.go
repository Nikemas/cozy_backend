package catalog

import (
	"context"
	"math"
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
		{"zero price_override", VariantInput{Size: "42", Color: "black", PriceOverride: f64(0)}},
		{"price_override rounding to 0.00", VariantInput{Size: "42", Color: "black", PriceOverride: f64(0.004)}},
		{"NaN price_override", VariantInput{Size: "42", Color: "black", PriceOverride: f64(math.NaN())}},
		{"Inf price_override", VariantInput{Size: "42", Color: "black", PriceOverride: f64(math.Inf(1))}},
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

func f64(v float64) *float64 { return &v }

// nil = no override (base price applies); a positive amount is valid.
func TestVariantInputValidateAcceptsNilAndPositiveOverride(t *testing.T) {
	for _, p := range []*float64{nil, f64(0.01), f64(5200)} {
		if err := (VariantInput{Size: "42", Color: "black", PriceOverride: p}).validate(); err != nil {
			t.Errorf("validate(%v) = %v, want nil", p, err)
		}
	}
}
