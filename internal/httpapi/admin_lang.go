package httpapi

import (
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/locales"
)

// adminLangCookie is the admin panel's RU/KY switch (internal/admin sets
// it with Path=/admin, so it reaches /admin/api/* too).
const adminLangCookie = "admin_lang"

// adminRequestLang is the language of admin-facing files (Excel exports,
// the import template): ?lang=, then the admin_lang cookie — the
// language the staff member picked in the panel, which must beat the
// browser's own Accept-Language for a plain download link — then
// apperr.LangFromRequest's Accept-Language/default.
func adminRequestLang(r *http.Request) string {
	if lang, ok := i18n.Normalize(r.URL.Query().Get("lang")); ok {
		return lang
	}
	if c, err := r.Cookie(adminLangCookie); err == nil {
		if lang, ok := i18n.Normalize(c.Value); ok {
			return lang
		}
	}
	return apperr.LangFromRequest(r)
}

// adminText returns key from locales/admin.<lang>.yaml (Russian when the
// language lacks it).
func adminText(lang, key string) string {
	return locales.AdminBundle().T(lang, key)
}
