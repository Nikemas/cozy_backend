package web

import (
	"regexp"
	"strings"
)

// (productSlugUUID/ProductPath/ResolveProductID below extend Foundation's
// Slugify with product-specific helpers — Task 2.)

// cyrillicToLatin is a fixed transliteration table (RU + a few KY-only
// letters) — good enough for the handful of demo rows Foundation ships
// with. Task 2 is free to swap it for a proper transliteration library
// once real catalog data needs better coverage.
var cyrillicToLatin = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	'ң': "ng", 'ө': "o", 'ү': "u",
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify converts a product/category name (name_ru or name_ky) into a
// URL-safe slug for the /product/:slug and /catalog/:slug routes.
//
// Example: Slugify("Кроссовки Nike Air") == "krossovki-nike-air"
func Slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if lat, ok := cyrillicToLatin[r]; ok {
			b.WriteString(lat)
			continue
		}
		b.WriteRune(r)
	}
	return strings.Trim(slugNonAlnum.ReplaceAllString(b.String(), "-"), "-")
}

// productSlugUUID matches the UUID prefix ProductPath embeds at the front
// of a /product/:slug path value. Products have no persisted slug column
// (unlike categories, which do — see catalog.CategoryRepo.ResolveID), so
// the URL encodes the id itself plus a purely cosmetic, SEO-friendly
// suffix built from the product's name.
var productSlugUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// ProductPath builds the canonical /product/:slug URL for a product.
//
// Example: ProductPath("a1b2...-uuid", "Кроссовки Nike Air") ==
// "/product/a1b2...-uuid-krossovki-nike-air"
func ProductPath(id, name string) string {
	if s := Slugify(name); s != "" {
		return "/product/" + id + "-" + s
	}
	return "/product/" + id
}

// ResolveProductID extracts the product UUID from a /product/:slug path
// value produced by ProductPath. The cosmetic suffix (if any) is ignored.
// Returns ok=false if slug doesn't start with a UUID.
func ResolveProductID(slug string) (id string, ok bool) {
	m := productSlugUUID.FindString(slug)
	if m == "" {
		return "", false
	}
	return m, true
}
