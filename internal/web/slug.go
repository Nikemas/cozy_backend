package web

import (
	"regexp"
	"strings"
)

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
