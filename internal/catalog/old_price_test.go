package catalog

import (
	"errors"
	"math"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

func floatPtr(v float64) *float64 { return &v }

func TestHasDiscount(t *testing.T) {
	tests := []struct {
		name     string
		oldPrice *float64
		price    float64
		want     bool
	}{
		{"no old price", nil, 4500, false},
		{"old price above current", floatPtr(5500), 4500, true},
		{"old price equal to current", floatPtr(4500), 4500, false},
		{"old price below current (variant priced higher)", floatPtr(5000), 6000, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasDiscount(tc.oldPrice, tc.price); got != tc.want {
				t.Fatalf("HasDiscount = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidateOldPrice(t *testing.T) {
	tests := []struct {
		name     string
		oldPrice *float64
		base     float64
		wantCode string // "" = valid
	}{
		{"nil means no discount", nil, 4500, ""},
		{"above base price", floatPtr(5500), 4500, ""},
		{"equal to base price", floatPtr(4500), 4500, "old_price_not_above_price"},
		{"below base price", floatPtr(4000), 4500, "old_price_not_above_price"},
		{"zero", floatPtr(0), 4500, "invalid_old_price"},
		{"negative", floatPtr(-1), 0, "invalid_old_price"},
		{"NaN", floatPtr(math.NaN()), 4500, "invalid_old_price"},
		{"Inf", floatPtr(math.Inf(1)), 4500, "invalid_old_price"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateOldPrice(tc.oldPrice, tc.base)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			var ae *apperr.AppError
			if !errors.As(err, &ae) || ae.Code != tc.wantCode {
				t.Fatalf("err = %v, want code %q", err, tc.wantCode)
			}
		})
	}
}

func TestProductInputValidateChecksOldPrice(t *testing.T) {
	in := ProductInput{CategoryID: "c1", NameRu: "Кеды", NameKy: "Кеды", BasePrice: 4500, OldPrice: floatPtr(4000)}
	if err := in.validate(); err == nil {
		t.Fatal("validate accepted an old price below the base price")
	}
	in.OldPrice = floatPtr(5000)
	if err := in.validate(); err != nil {
		t.Fatalf("validate rejected a valid discount: %v", err)
	}
}
