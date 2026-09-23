package media

import (
	"strings"
	"testing"
)

func TestNewVariantKeysShareAUniquePrefix(t *testing.T) {
	full1, thumb1 := newVariantKeys()
	full2, _ := newVariantKeys()

	if full1 == full2 {
		t.Fatalf("newVariantKeys returned the same key twice: %q", full1)
	}
	if !strings.HasPrefix(full1, "products/") || !strings.HasSuffix(full1, "/full.jpg") {
		t.Errorf("full key = %q, want products/<uuid>/full.jpg", full1)
	}
	if thumb1 != strings.TrimSuffix(full1, "/full.jpg")+"/thumb.jpg" {
		t.Errorf("thumb key %q doesn't share the full key's prefix (%q)", thumb1, full1)
	}
	if got := ThumbKey(full1); got != thumb1 {
		t.Errorf("ThumbKey(%q) = %q, want %q", full1, got, thumb1)
	}
}

func TestThumbKey(t *testing.T) {
	cases := map[string]string{
		"products/abc/full.jpg": "products/abc/thumb.jpg",
		// Legacy single-file keys are served unchanged.
		"products/abc.jpg":  "products/abc.jpg",
		"products/abc.png":  "products/abc.png",
		"products/abc.webp": "products/abc.webp",
		// Only the exact "/full.jpg" suffix counts.
		"products/full.jpg":         "products/thumb.jpg",
		"products/abcfull.jpg":      "products/abcfull.jpg",
		"products/abc/full.jpg.bak": "products/abc/full.jpg.bak",
		"":                          "",
	}
	for in, want := range cases {
		if got := ThumbKey(in); got != want {
			t.Errorf("ThumbKey(%q) = %q, want %q", in, got, want)
		}
	}
}
