package i18n

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// kyColors maps a normalized Russian color name (lower case, ё → е, see
// colorKey) to its Kyrgyz display label. Product colors are stored in
// Russian (product_variants.color) and stay that way — filters and forms
// keep using the raw value; this is only what a Kyrgyz page shows.
// The Kyrgyz words are drafts pending native proofreading
// (tasks/ky-review-2026-10-08.md, «Цвета (color labels)»).
var kyColors = map[string]string{
	"черный":       "кара",
	"белый":        "ак",
	"серый":        "боз",
	"бежевый":      "беж",
	"коричневый":   "күрөң",
	"синий":        "көк",
	"голубой":      "көгүлтүр",
	"красный":      "кызыл",
	"розовый":      "кызгылт",
	"зеленый":      "жашыл",
	"желтый":       "сары",
	"оранжевый":    "кызгылт сары",
	"фиолетовый":   "кызгылт көк",
	"бордовый":     "кочкул кызыл",
	"хаки":         "хаки",
	"молочный":     "сүт түстүү",
	"кремовый":     "каймак түстүү",
	"пудровый":     "упа түстүү",
	"золотой":      "алтын түстүү",
	"серебристый":  "күмүш түстүү",
	"бронзовый":    "коло түстүү",
	"разноцветный": "түркүн түстүү",
	"мультиколор":  "түркүн түстүү",
	"бирюзовый":    "бирюза түстүү",
	"мятный":       "жалбыз түстүү",
	"песочный":     "кум түстүү",
	"оливковый":    "зайтун түстүү",
	"графитовый":   "графит түстүү",
}

// kyShadePrefixes are the "тёмно-"/"светло-" compounds: "тёмно-синий" is
// the shade word plus the base color's label ("кочкул көк").
var kyShadePrefixes = []struct{ ru, ky string }{
	{"темно-", "кочкул "},
	{"светло-", "ачык "},
}

// ColorLabel returns how a stored (Russian) product color should be shown
// in lang. For anything but Kyrgyz, and for colors the dictionary does not
// know, it returns color unchanged. Matching ignores case, surrounding
// spaces and ё/е; the result follows the input's capitalization (all caps,
// capitalized, or lower case).
func ColorLabel(lang, color string) string {
	if lang != LangKY {
		return color
	}
	label, ok := lookupKYColor(colorKey(color))
	if !ok {
		return color
	}
	return matchCase(strings.TrimSpace(color), label)
}

func lookupKYColor(key string) (string, bool) {
	if label, ok := kyColors[key]; ok {
		return label, true
	}
	for _, p := range kyShadePrefixes {
		base, found := strings.CutPrefix(key, p.ru)
		if !found {
			continue
		}
		if label, ok := kyColors[base]; ok {
			return p.ky + label, true
		}
	}
	return "", false
}

// colorKey normalizes a color for dictionary lookup: trimmed, lower case,
// ё → е, inner whitespace collapsed and none around hyphens.
func colorKey(color string) string {
	key := strings.ToLower(strings.Join(strings.Fields(color), " "))
	key = strings.ReplaceAll(key, "ё", "е")
	key = strings.ReplaceAll(key, " - ", "-")
	key = strings.ReplaceAll(key, "- ", "-")
	return strings.ReplaceAll(key, " -", "-")
}

// matchCase gives label (lower case) the capitalization style of input.
func matchCase(input, label string) string {
	first, _ := utf8.DecodeRuneInString(input)
	if !unicode.IsUpper(first) {
		return label
	}
	if utf8.RuneCountInString(input) > 1 && input == strings.ToUpper(input) {
		return strings.ToUpper(label)
	}
	r, size := utf8.DecodeRuneInString(label)
	return string(unicode.ToUpper(r)) + label[size:]
}
