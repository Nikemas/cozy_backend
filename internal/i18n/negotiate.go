package i18n

import (
	"strconv"
	"strings"
)

// Normalize maps a language tag to a supported language code: "ru",
// "ru-RU" -> ru; "ky", "ky-KG", "kir" -> ky. ok is false for anything
// else (including "" and "*").
func Normalize(tag string) (lang string, ok bool) {
	primary := strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(primary, "-_"); i >= 0 {
		primary = primary[:i]
	}
	switch primary {
	case LangRU, "rus":
		return LangRU, true
	case LangKY, "kir":
		return LangKY, true
	}
	return "", false
}

// FromAcceptLanguage picks the supported language a request's
// Accept-Language header prefers most (RFC 9110 §12.5.4): the highest
// q-value wins, ties go to the earlier entry, q=0 means "not acceptable".
// ok is false when the header names no supported language — the caller
// then falls back to its next source (cookie, default).
func FromAcceptLanguage(header string) (lang string, ok bool) {
	bestQ := 0.0
	for _, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(part, ";")
		candidate, supported := Normalize(tag)
		if !supported {
			continue
		}
		q := qValue(params)
		if q > bestQ {
			lang, bestQ, ok = candidate, q, true
		}
	}
	return lang, ok
}

// qValue reads "q=0.8" out of an Accept-Language entry's parameters; a
// missing or malformed q counts as 1 (malformed ones are rare and
// treating them as "most preferred" matches what browsers send anyway).
func qValue(params string) float64 {
	for _, p := range strings.Split(params, ";") {
		name, value, found := strings.Cut(strings.TrimSpace(p), "=")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || q > 1 {
			return 1
		}
		if q < 0 {
			return 0
		}
		return q
	}
	return 1
}

// Fill substitutes {name} placeholders in s with params[name]. Unknown
// placeholders are left as-is, so a missing parameter is visible rather
// than silently dropped.
func Fill(s string, params map[string]string) string {
	if len(params) == 0 || !strings.Contains(s, "{") {
		return s
	}
	pairs := make([]string, 0, 2*len(params))
	for k, v := range params {
		pairs = append(pairs, "{"+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(s)
}
