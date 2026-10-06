package web

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

var (
	localeFormatVerb  = regexp.MustCompile(`%[sdvq]`)
	localePlaceholder = regexp.MustCompile(`\{[a-z_]+\}`)
	pluralSuffixes    = []string{".one", ".few", ".many"}
)

// TestLocaleValuesParity complements TestLocaleKeySetsMatch: every
// storefront value is non-empty in both languages, carries the same
// fmt verbs and {placeholders} in ru.yaml and ky.yaml (a missing %s in
// ky.yaml would print "%!(EXTRA ...)" on the KY site), and every plural
// group defines all of .one/.few/.many.
func TestLocaleValuesParity(t *testing.T) {
	b, err := i18n.Load(filepath.Join(repoRoot(t), "locales"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range b.Keys(i18n.LangRU) {
		ru, ky := b.T(i18n.LangRU, key), b.T(i18n.LangKY, key)
		if strings.TrimSpace(ru) == "" || strings.TrimSpace(ky) == "" {
			t.Errorf("%s: empty value (ru=%q, ky=%q)", key, ru, ky)
		}
		if a, c := localeFormatVerb.FindAllString(ru, -1), localeFormatVerb.FindAllString(ky, -1); !slices.Equal(a, c) {
			t.Errorf("%s: format verbs ru %v, ky %v", key, a, c)
		}
		a, c := localePlaceholder.FindAllString(ru, -1), localePlaceholder.FindAllString(ky, -1)
		slices.Sort(a)
		slices.Sort(c)
		if !slices.Equal(a, c) {
			t.Errorf("%s: placeholders ru %v, ky %v", key, a, c)
		}
		assertPluralGroupComplete(t, b, key)
	}
}

func assertPluralGroupComplete(t *testing.T, b *i18n.Bundle, key string) {
	t.Helper()
	for _, suffix := range pluralSuffixes {
		base, ok := strings.CutSuffix(key, suffix)
		if !ok {
			continue
		}
		for _, form := range pluralSuffixes {
			for _, lang := range []string{i18n.LangRU, i18n.LangKY} {
				if !b.Has(lang, base+form) {
					t.Errorf("%s: plural group %q lacks %s in %s", key, base, form, lang)
				}
			}
		}
	}
}
