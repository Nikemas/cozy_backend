package admin

import (
	"strings"
	"testing"
)

func TestResolveProductsColor(t *testing.T) {
	colors := []string{"Белый", "Чёрный"}

	sel, opts := resolveProductsColor(ruTr, colors, "Чёрный")
	if sel != "Чёрный" || len(opts) != 3 || opts[0].Value != "" || !opts[2].Selected || opts[1].Selected {
		t.Fatalf("sel=%q opts=%+v", sel, opts)
	}

	// An unknown color is ignored rather than filtering on it.
	if sel, opts := resolveProductsColor(ruTr, colors, "Розовый"); sel != "" || !opts[0].Selected {
		t.Fatalf("unknown color: sel=%q opts=%+v", sel, opts)
	}

	// Labels follow the admin language; the value stays the stored color.
	_, ky := resolveProductsColor(trFor("ky"), colors, "")
	if ky[2].Value != "Чёрный" || ky[2].Label == "Чёрный" || ky[0].Label != "Бардык түстөр" {
		t.Fatalf("ky options = %+v", ky)
	}
}

func TestWithColorParamKeepsOtherParams(t *testing.T) {
	in := []ChipVM{{URL: "/admin/products?cat=men&point=p1"}}
	out := withColorParam(in, "Чёрный")
	if !strings.Contains(out[0].URL, "color=") || !strings.Contains(out[0].URL, "point=p1") || !strings.Contains(out[0].URL, "cat=men") {
		t.Fatalf("url = %q", out[0].URL)
	}
	if in[0].URL != "/admin/products?cat=men&point=p1" {
		t.Fatal("input chips were mutated")
	}
	if got := withColorParam(in, ""); got[0].URL != in[0].URL {
		t.Fatalf("no color: url = %q", got[0].URL)
	}
}
