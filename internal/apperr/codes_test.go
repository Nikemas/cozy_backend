package apperr

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// errorSite is one apperr constructor call found in the source.
type errorSite struct {
	pos     string
	code    string
	variant string
	message *string         // nil unless the message is a string literal
	params  map[string]bool // keys of a WithParams(map[string]string{...}) literal; nil if none
}

func (s errorSite) key() string {
	if s.variant == "" {
		return "err." + s.code
	}
	return "err." + s.code + "." + s.variant
}

var constructorArgs = map[string]int{ // constructor -> index of the code argument
	"New": 1, "NotFound": 0, "BadRequest": 0, "Unauthorized": 0,
	"Forbidden": 0, "Conflict": 0, "TooManyRequests": 0,
}

// scanErrorSites parses every non-test .go file under the module's
// internal/ and cmd/ trees and returns each apperr.<Constructor>(...)
// call with its code, its WithVariant/WithParams wrapping and (when
// literal) its Russian message.
func scanErrorSites(t *testing.T) []errorSite {
	t.Helper()
	var sites []errorSite
	for _, root := range []string{"..", filepath.Join("..", "..", "cmd")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			sites = append(sites, scanFile(t, path)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if len(sites) < 100 { // sanity: the scan must actually find the codebase's errors
		t.Fatalf("found only %d apperr call sites — is the scan rooted correctly?", len(sites))
	}
	return sites
}

func scanFile(t *testing.T, path string) []errorSite {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	// Wrapping first: constructor call -> variant / params it is chained with.
	variants := map[*ast.CallExpr]string{}
	params := map[*ast.CallExpr]map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "WithVariant" && sel.Sel.Name != "WithParams") || len(call.Args) != 1 {
			return true
		}
		ctor := innermostConstructor(sel.X)
		if ctor == nil {
			return true
		}
		if sel.Sel.Name == "WithVariant" {
			lit, ok := stringLit(call.Args[0])
			if !ok {
				t.Errorf("%s: WithVariant needs a string literal (the locale test reads it)", fset.Position(call.Pos()))
			}
			variants[ctor] = lit
		} else {
			params[ctor] = mapLitKeys(call.Args[0])
		}
		return true
	})

	var sites []errorSite
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isConstructor(call) {
			return true
		}
		idx := constructorArgs[call.Fun.(*ast.SelectorExpr).Sel.Name]
		pos := fset.Position(call.Pos()).String()
		code, ok := stringLit(call.Args[idx])
		if !ok {
			t.Errorf("%s: error code must be a string literal", pos)
			return true
		}
		s := errorSite{pos: pos, code: code, variant: variants[call], params: params[call]}
		if msg, ok := stringLit(call.Args[idx+1]); ok {
			s.message = &msg
		}
		sites = append(sites, s)
		return true
	})
	return sites
}

func isConstructor(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "apperr" {
		return false
	}
	idx, ok := constructorArgs[sel.Sel.Name]
	return ok && len(call.Args) == idx+2
}

// innermostConstructor unwraps x.WithVariant(..).WithParams(..) chains
// down to the apperr constructor call, nil if there is none.
func innermostConstructor(x ast.Expr) *ast.CallExpr {
	for {
		call, ok := x.(*ast.CallExpr)
		if !ok {
			return nil
		}
		if isConstructor(call) {
			return call
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return nil
		}
		x = sel.X
	}
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

func mapLitKeys(e ast.Expr) map[string]bool {
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	keys := map[string]bool{}
	for _, el := range lit.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if k, ok := stringLit(kv.Key); ok {
				keys[k] = true
			}
		}
	}
	return keys
}

var placeholderRE = regexp.MustCompile(`\{([a-z_]+)\}`)

func placeholders(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range placeholderRE.FindAllStringSubmatch(s, -1) {
		out[m[1]] = true
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestEveryErrorCodeIsTranslated: every code (and code variant) raised
// anywhere in the code has a message in both errors.ru.yaml and
// errors.ky.yaml.
func TestEveryErrorCodeIsTranslated(t *testing.T) {
	keys := map[string]string{"err.internal_error": "apperr.Internal"}
	for _, s := range scanErrorSites(t) {
		keys[s.key()] = s.pos
		keys["err."+s.code] = s.pos // the code's default message must exist too
	}
	for key, pos := range keys {
		for _, lang := range []string{i18n.LangRU, i18n.LangKY} {
			if !messages.Has(lang, key) {
				t.Errorf("%s: no %q in locales/errors.%s.yaml", pos, key, lang)
			}
		}
	}

	// And no stale keys: every err.* key is still raised somewhere.
	for _, key := range messages.Keys(i18n.LangRU) {
		if strings.HasPrefix(key, "err.") {
			if _, used := keys[key]; !used {
				t.Errorf("locales/errors.ru.yaml: %q is not raised anywhere — remove it", key)
			}
		}
	}
}

// TestRussianErrorMessagesMatchCode keeps errors.ru.yaml identical to the
// literal Russian messages in the code (Russian responses are served from
// Message, so the YAML must not drift from it), and checks that the
// placeholders of every translation are exactly the keys the call site
// passes to WithParams.
func TestRussianErrorMessagesMatchCode(t *testing.T) {
	for _, s := range scanErrorSites(t) {
		ru := messages.T(i18n.LangRU, s.key())
		if s.message != nil && s.params == nil && ru != *s.message {
			t.Errorf("%s: errors.ru.yaml %s = %q, code says %q", s.pos, s.key(), ru, *s.message)
		}
		for _, lang := range []string{i18n.LangRU, i18n.LangKY} {
			got := placeholders(messages.T(lang, s.key()))
			want := s.params
			if want == nil {
				want = map[string]bool{}
			}
			if strings.Join(sortedKeys(got), ",") != strings.Join(sortedKeys(want), ",") {
				t.Errorf("%s: %s (%s) has placeholders %v, call site passes %v", s.pos, s.key(), lang, sortedKeys(got), sortedKeys(want))
			}
		}
	}
}

func TestErrorLocalesHaveSameKeys(t *testing.T) {
	ru, ky := messages.Keys(i18n.LangRU), messages.Keys(i18n.LangKY)
	if strings.Join(ru, "\n") != strings.Join(ky, "\n") {
		t.Errorf("errors.ru.yaml and errors.ky.yaml key sets differ:\nru=%v\nky=%v", ru, ky)
	}
	for _, key := range ru {
		r, k := placeholders(messages.T(i18n.LangRU, key)), placeholders(messages.T(i18n.LangKY, key))
		if strings.Join(sortedKeys(r), ",") != strings.Join(sortedKeys(k), ",") {
			t.Errorf("%s: placeholders differ: ru %v, ky %v", key, sortedKeys(r), sortedKeys(k))
		}
	}
}
