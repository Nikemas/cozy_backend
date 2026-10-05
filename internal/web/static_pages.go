package web

import (
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/config"
)

// StaticPageData is the .Data of the legal/info pages: the shop's contact
// details and app store links from the environment. Templates render a
// line only when its value is set, so an unconfigured field never shows
// up as a placeholder on a public page.
type StaticPageData struct {
	Contacts        config.Contacts
	StoreURLIOS     string
	StoreURLAndroid string
	// AccountDeleted shows the "your account was deleted" notice on
	// /account-deletion right after POST /account/delete succeeded.
	AccountDeleted bool
	// OTPRetentionDays is how long SMS-code request records are kept
	// (OTP_RETENTION_DAYS) — stated on /privacy and /account-deletion.
	OTPRetentionDays int
}

// staticPage serves one of the legal/info pages (see render.go's
// staticPages): /about, /contacts, /delivery, /privacy, /terms,
// /account-deletion. The mobile app links to <apiBase>/privacy and Google
// Play to /account-deletion, so these must stay public and stable.
func (h *handlers) staticPage(name string) func(http.ResponseWriter, *http.Request) error {
	screen := "page_" + name
	return func(w http.ResponseWriter, r *http.Request) error {
		data := h.base(r, screen)
		pageData := h.staticPageData()
		pageData.AccountDeleted = name == accountDeletionPage && r.URL.Query().Get("deleted") == "1"
		data.Data = pageData
		return h.render.Render(w, screen, data)
	}
}

func (h *handlers) staticPageData() StaticPageData {
	if h.cfg == nil {
		return StaticPageData{OTPRetentionDays: config.DefaultOTPRetentionDays}
	}
	days := h.cfg.Security.Auth.OTPRetentionDays
	if days <= 0 {
		days = config.DefaultOTPRetentionDays
	}
	return StaticPageData{
		Contacts:         h.cfg.Contacts,
		StoreURLIOS:      h.cfg.AppStoreURLIOS,
		StoreURLAndroid:  h.cfg.AppStoreURLAndroid,
		OTPRetentionDays: days,
	}
}
