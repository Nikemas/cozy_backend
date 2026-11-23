package web

import (
	"net/url"
	"reflect"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/catalog"
)

func TestBuildVariantPickers(t *testing.T) {
	variants := []catalog.Variant{
		{ID: "v1", Size: "40", Color: "Белый"},
		{ID: "v2", Size: "41", Color: "Белый"},
		{ID: "v3", Size: "41", Color: "Чёрный"},
		{ID: "v4", Size: "42", Color: "Чёрный"},
		{ID: "v5", Size: "42", Color: "Красный"},
	}
	qty := map[string]int{"v1": 2, "v3": 2, "v4": 2}

	labels := func(opts []ColorOption) []string {
		out := make([]string, len(opts))
		for i, o := range opts {
			out[i] = o.Label
		}
		return out
	}

	t.Run("colors are only those of the selected size", func(t *testing.T) {
		p := buildVariantPickers(variants, qty, url.Values{"size": {"40"}}, "/p", "ru")
		if p.SelectedSize != "40" || p.SelectedColor != "Белый" {
			t.Fatalf("selected %q/%q", p.SelectedSize, p.SelectedColor)
		}
		if got := labels(p.Colors); !reflect.DeepEqual(got, []string{"Белый"}) {
			t.Fatalf("colors = %v", got)
		}
	})

	t.Run("color missing in the new size moves to its first in-stock color", func(t *testing.T) {
		p := buildVariantPickers(variants, qty, url.Values{"size": {"42"}, "color": {"Белый"}}, "/p", "ru")
		if p.SelectedColor != "Чёрный" {
			t.Fatalf("color = %q, want Чёрный", p.SelectedColor)
		}
		if len(p.Colors) != 2 || p.Colors[1].Available {
			t.Fatalf("sold-out Красный must be listed but unavailable: %+v", p.Colors)
		}
	})

	t.Run("sold-out color in the size moves to an in-stock one", func(t *testing.T) {
		p := buildVariantPickers(variants, qty, url.Values{"size": {"41"}, "color": {"Белый"}}, "/p", "ru")
		if p.SelectedColor != "Чёрный" {
			t.Fatalf("color = %q, want Чёрный", p.SelectedColor)
		}
	})

	t.Run("size is available when any color has stock", func(t *testing.T) {
		p := buildVariantPickers(variants, qty, url.Values{"size": {"42"}}, "/p", "ru")
		for _, s := range p.Sizes {
			if !s.Available {
				t.Fatalf("size %s should be available", s.Label)
			}
		}
	})

	t.Run("size without stock is unavailable", func(t *testing.T) {
		v := []catalog.Variant{{ID: "a", Size: "40", Color: "Белый"}, {ID: "b", Size: "41", Color: "Белый"}}
		p := buildVariantPickers(v, map[string]int{"a": 1}, nil, "/p", "ru")
		if p.Sizes[1].Available {
			t.Fatal("size 41 has no stock")
		}
	})
}
