package apperr

import (
	"net/http"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/locales"
)

// Error messages in the client's language.
//
// AppError.Message stays the Russian source text (logs, tests and the
// HTML error pages keep using it). For JSON errors WriteError replaces
// it with Localize(LangFromRequest(r), err):
//
//   - the text lives in locales/errors.ru.yaml / errors.ky.yaml under
//     MessageKey() — "err.<code>" or "err.<code>.<variant>";
//   - {name} placeholders are filled from the error's params;
//   - Russian is served from Message itself, so the Russian API output is
//     byte-for-byte what it was before translations existed (a test keeps
//     errors.ru.yaml in sync with the literal messages in the code);
//   - a code without a translation keeps its Message: a new code is
//     never broken, just Russian until someone adds its keys.
//
// Code and Status are never translated — clients branch on them. The
// response's Content-Language names the language message is really in
// ("ru" whenever it fell back to the Russian Message).

const (
	// siteLangCookie / adminLangCookie are the language cookies the
	// storefront (internal/web) and the admin panel (internal/admin) set
	// from their RU/KY switchers.
	siteLangCookie  = "cozy_lang"
	adminLangCookie = "admin_lang"
)

// messages is the error-message bundle. A broken embedded file is a
// build-time mistake, so it fails loudly at startup.
var messages = mustLoadMessages()

func mustLoadMessages() *i18n.Bundle {
	b, err := i18n.LoadFS(locales.Errors, "errors.%s.yaml")
	if err != nil {
		panic(err)
	}
	return b
}

// LangFromRequest picks the language for r's error messages: ?lang=,
// then the Accept-Language header (ru|ky, by q-value), then the language
// cookie (admin_lang under /admin, cozy_lang elsewhere), then Russian.
// Unsupported values at any step fall through to the next one.
func LangFromRequest(r *http.Request) string {
	if r == nil {
		return i18n.DefaultLang
	}
	if lang, ok := i18n.Normalize(r.URL.Query().Get("lang")); ok {
		return lang
	}
	if lang, ok := i18n.FromAcceptLanguage(r.Header.Get("Accept-Language")); ok {
		return lang
	}
	if c, err := r.Cookie(langCookieFor(r.URL.Path)); err == nil {
		if lang, ok := i18n.Normalize(c.Value); ok {
			return lang
		}
	}
	return i18n.DefaultLang
}

func langCookieFor(path string) string {
	if path == "/admin" || strings.HasPrefix(path, "/admin/") {
		return adminLangCookie
	}
	return siteLangCookie
}

// Localize returns e's message in lang (see the comment at the top):
// Message for Russian or when lang has no translation for e's key.
func Localize(lang string, e *AppError) string {
	msg, _ := localize(lang, e)
	return msg
}

// localize is Localize that also reports the language the returned
// message is actually written in — lang, or Russian when it fell back to
// Message. WriteError sends it as Content-Language, so a client can tell
// a real Kyrgyz message from an untranslated Russian one.
func localize(lang string, e *AppError) (msg, msgLang string) {
	if e == nil {
		return "", i18n.DefaultLang
	}
	if lang == i18n.DefaultLang || !messages.Has(lang, e.MessageKey()) {
		return e.Message, i18n.DefaultLang
	}
	return i18n.Fill(messages.T(lang, e.MessageKey()), e.params), lang
}

// Translate returns key from the error-message bundle in lang (Russian
// if lang lacks it, the key itself if both do) with params filled — for
// texts that are not an AppError, such as the per-row messages of the
// product import report.
func Translate(lang, key string, params map[string]string) string {
	return i18n.Fill(messages.T(lang, key), params)
}

// HasTranslation reports whether key exists in lang's error messages
// (no fallback) — for tests that check coverage.
func HasTranslation(lang, key string) bool { return messages.Has(lang, key) }
