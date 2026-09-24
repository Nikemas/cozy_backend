package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/locales"
)

// Admin panel languages (Приложение №2 Б.3: русский и кыргызский).
//
// How it fits together:
//
//   - Strings live in locales/admin.ru.yaml / admin.ky.yaml (flat
//     "admin.<area>.<name>: text"), embedded into the binary by package
//     locales and loaded once into adminBundle.
//   - Templates call {{t "admin.key"}} (or {{tf "admin.key" args...}} for
//     a fmt.Sprintf-style format). NewRenderer parses every screen once and
//     clones it per language with that language's "t"/"tf"/"lang" funcs.
//   - The language is the admin_lang cookie (langFromRequest), switched by
//     POST /admin/lang from the RU/KY switcher in the header and on the
//     login page.
//   - Render picks the language from PageData.Lang if set, else from the
//     ResponseWriter the auth-gate wrapped (withLang) — so every page behind
//     requireStaffRole renders in the staff member's language without its
//     handler doing anything. Go-side text (flash/toast/errors/labels) goes
//     through h.tr(r).T("admin.key").
//   - Chrome fields that hold text (PageData.PageTitle, NavItem.Label,
//     PageData.RoleLabel) may hold either a key or ready text: templates
//     print them through {{t ...}}, and T returns unknown strings as-is.

const (
	langCookieName   = "admin_lang"
	langCookieMaxAge = 365 * 24 * 60 * 60
)

// adminBundle is the admin panel's translation set. A broken/missing
// embedded file is a build-time mistake, so it fails loudly at startup.
var adminBundle = mustLoadAdminBundle()

func mustLoadAdminBundle() *i18n.Bundle {
	b, err := i18n.LoadFS(locales.Admin, "admin.%s.yaml")
	if err != nil {
		panic(err)
	}
	return b
}

// supportedLang normalizes lang to a supported language code.
func supportedLang(lang string) string {
	if lang == i18n.LangKY {
		return i18n.LangKY
	}
	return i18n.LangRU
}

// langFromRequest returns the admin language: the one the auth-gate put
// in r's context, else the admin_lang cookie, else Russian.
func langFromRequest(r *http.Request) string {
	if r == nil {
		return i18n.DefaultLang
	}
	if lang, ok := r.Context().Value(langCtxKey{}).(string); ok {
		return lang
	}
	if c, err := r.Cookie(langCookieName); err == nil {
		return supportedLang(c.Value)
	}
	return i18n.DefaultLang
}

// tr translates admin strings into one language. The zero value is
// Russian, so helpers called without a request (tests) keep producing the
// original Russian text.
type tr struct{ lang string }

// ruTr is the Russian translator, for call sites with no request.
var ruTr = tr{lang: i18n.LangRU}

func trFor(lang string) tr { return tr{lang: supportedLang(lang)} }

// tr returns the translator for r's admin language.
func (h *handlers) tr(r *http.Request) tr { return trFor(langFromRequest(r)) }

type langCtxKey struct{}

// contextWithLang records the admin language in ctx (done by the
// auth-gate), so view builders that only get a ctx can translate.
func contextWithLang(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, langCtxKey{}, supportedLang(lang))
}

// trFromContext returns the translator for the language in ctx (Russian
// if none — e.g. a view builder called directly from a test).
func trFromContext(ctx context.Context) tr {
	lang, _ := ctx.Value(langCtxKey{}).(string)
	return trFor(lang)
}

// trFromWriter returns the translator for the language the auth-gate
// attached to w (Russian if none) — for error helpers that get no request.
func trFromWriter(w http.ResponseWriter) tr { return trFor(langFromWriter(w)) }

// Lang is the language code ("ru"/"ky").
func (t tr) Lang() string { return supportedLang(t.lang) }

// T returns key's translation (Russian if missing in the language; the
// key itself if missing everywhere — easy to spot, never a broken page).
func (t tr) T(key string) string { return adminBundle.T(t.Lang(), key) }

// F is T used as a fmt.Sprintf format.
func (t tr) F(key string, args ...any) string {
	return fmt.Sprintf(t.T(key), args...)
}

// Plural picks the word form for n: keys <base>.one / .few / .many. Russian
// uses its one/few/many rule; Kyrgyz nouns don't change after a numeral,
// so all three Kyrgyz forms are the same word.
func (t tr) Plural(n int, base string) string {
	if t.Lang() == i18n.LangRU {
		return pluralRu(n, t.T(base+".one"), t.T(base+".few"), t.T(base+".many"))
	}
	return t.T(base + ".many")
}

// N renders "<n> <plural form>", e.g. "5 товаров" / "5 товар".
func (t tr) N(n int, base string) string {
	return fmt.Sprintf("%d %s", n, t.Plural(n, base))
}

// templateFuncs are the per-language template functions. Parse-time
// placeholders use Russian; NewRenderer's per-language clones replace them.
func templateFuncs(lang string) map[string]any {
	t := trFor(lang)
	return map[string]any{
		"t":    t.T,
		"tf":   t.F,
		"lang": t.Lang,
	}
}

// langWriter carries the request's admin language to Renderer.Render,
// whose signature (w, screen, data) predates the language switch.
type langWriter struct {
	http.ResponseWriter
	lang string
}

func (lw *langWriter) adminLang() string { return lw.lang }

// Unwrap lets http.ResponseController reach the underlying writer.
func (lw *langWriter) Unwrap() http.ResponseWriter { return lw.ResponseWriter }

// withLang wraps w so Render knows r's language (see requireStaffRole).
func withLang(w http.ResponseWriter, r *http.Request) http.ResponseWriter {
	return &langWriter{ResponseWriter: w, lang: langFromRequest(r)}
}

// langFromWriter returns the language withLang attached to w, or "".
func langFromWriter(w http.ResponseWriter) string {
	if lw, ok := w.(interface{ adminLang() string }); ok {
		return lw.adminLang()
	}
	return ""
}

// setLang handles POST /admin/lang (form: lang=ru|ky, next=<admin path>):
// stores the choice in the admin_lang cookie and goes back to next (or
// the Referer), only ever redirecting inside /admin.
func (h *handlers) setLang(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	secure := h.cfg != nil && h.cfg.Security.CookieSecure
	http.SetCookie(w, &http.Cookie{
		Name:     langCookieName,
		Value:    supportedLang(r.FormValue("lang")),
		Path:     "/admin",
		MaxAge:   langCookieMaxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, safeAdminNext(r.FormValue("next"), r.Referer()), http.StatusSeeOther)
}

// safeAdminNext returns next if it is a local /admin path, else the path
// of referer if that is one, else /admin/login (which forwards a signed-in
// staff member to their first page).
func safeAdminNext(next, referer string) string {
	if isLocalAdminPath(next) {
		return next
	}
	if i := strings.Index(referer, "://"); i >= 0 {
		rest := referer[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 && isLocalAdminPath(rest[j:]) {
			return rest[j:]
		}
	}
	return "/admin/login"
}

func isLocalAdminPath(p string) bool {
	if p != "/admin" && !strings.HasPrefix(p, "/admin/") && !strings.HasPrefix(p, "/admin?") {
		return false
	}
	return !strings.ContainsAny(p, "\\\r\n") && !strings.HasPrefix(p, "//")
}

// localizedError is a validation error whose text is a locale key, so
// pure parsing helpers (no request, no language) can still return errors
// the page shows in the staff member's language via errText. Error()
// is the Russian text, as before.
type localizedError struct{ key string }

func (e localizedError) Error() string { return ruTr.T(e.key) }

// errText renders err in t's language when it is a localizedError, else
// err.Error().
func errText(t tr, err error) string {
	var le localizedError
	if errors.As(err, &le) {
		return t.T(le.key)
	}
	return err.Error()
}
