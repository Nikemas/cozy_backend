package i18n

import (
	"unicode"
	"unicode/utf8"
)

// UpperFirst capitalizes s's first letter when it is a lowercase Cyrillic
// letter (Russian or Kyrgyz) — for error texts that services write in
// lowercase (they are also composed mid-sentence) but that start a line
// when shown on their own. A Latin first word is an identifier
// ("point_id обязателен", "slug ...") and stays as written.
func UpperFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError || !unicode.Is(unicode.Cyrillic, r) || !unicode.IsLower(r) {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
