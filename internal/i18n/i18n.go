// Package i18n provides a minimal string-translation layer for the public
// website's html/template layer. Locale files live in locales/*.yaml as a
// flat "key: value" map — a deliberately small YAML subset (no nesting,
// lists, or multi-line scalars) so Task 1 doesn't need to pull in a YAML
// dependency for a handful of layout strings. Tasks 2-5 append their own
// screens' keys to both locale files as they build each screen.
package i18n

import (
	"bufio"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"
)

const (
	LangRU = "ru"
	LangKY = "ky"

	// DefaultLang is used whenever a request has no (or an unrecognized)
	// language cookie, and as the fallback when a key is missing from the
	// requested language's file.
	DefaultLang = LangRU
)

// Bundle holds every supported language's loaded translations.
type Bundle struct {
	mu    sync.RWMutex
	langs map[string]map[string]string
}

// Load reads "<dir>/<lang>.yaml" for every supported language and returns
// a Bundle. Both files must exist — Foundation ships both, and every
// later task is expected to keep them in sync.
func Load(dir string) (*Bundle, error) {
	b := &Bundle{langs: map[string]map[string]string{}}
	for _, lang := range []string{LangRU, LangKY} {
		path := dir + "/" + lang + ".yaml"
		m, err := parseFile(path)
		if err != nil {
			return nil, fmt.Errorf("i18n: loading %s: %w", path, err)
		}
		b.langs[lang] = m
	}
	return b, nil
}

// LoadFS reads one file per supported language from fsys, named by
// nameFormat with the language code substituted for its single %s (e.g.
// "admin.%s.yaml" -> admin.ru.yaml, admin.ky.yaml). It is how the admin
// panel loads its own key set (locales/admin.*.yaml, embedded by package
// locales) separately from the storefront's ru.yaml/ky.yaml, so the two
// surfaces never edit the same locale file.
func LoadFS(fsys fs.FS, nameFormat string) (*Bundle, error) {
	b := &Bundle{langs: map[string]map[string]string{}}
	for _, lang := range []string{LangRU, LangKY} {
		name := fmt.Sprintf(nameFormat, lang)
		f, err := fsys.Open(name)
		if err != nil {
			return nil, fmt.Errorf("i18n: loading %s: %w", name, err)
		}
		m, err := parse(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("i18n: parsing %s: %w", name, err)
		}
		b.langs[lang] = m
	}
	return b, nil
}

// Has reports whether key has a translation in lang itself (no fallback).
func (b *Bundle) Has(lang, key string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.langs[lang][key]
	return ok
}

// Keys returns lang's keys, sorted — for tests that check two languages
// define exactly the same key set.
func (b *Bundle) Keys(lang string) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	keys := make([]string, 0, len(b.langs[lang]))
	for k := range b.langs[lang] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// T returns the translation for key in lang, falling back to DefaultLang
// and then to the raw key so a missing translation never breaks a page —
// it just shows the key, which is easy to spot during review.
func (b *Bundle) T(lang, key string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if m, ok := b.langs[lang]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	if m, ok := b.langs[DefaultLang]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return key
}

// FuncMap returns a html/template.FuncMap exposing {{t "key"}} bound to
// lang, ready to merge into a page's template.FuncMap.
func (b *Bundle) FuncMap(lang string) template.FuncMap {
	return template.FuncMap{
		"t": func(key string) string { return b.T(lang, key) },
	}
}

func parseFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return parse(f)
}

func parse(r io.Reader) (map[string]string, error) {
	m := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"`)
		m[key] = val
	}
	return m, sc.Err()
}
