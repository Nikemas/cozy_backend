// Package locales embeds the admin panel's translation files
// (admin.ru.yaml / admin.ky.yaml) into the binary, so internal/admin gets
// its strings without depending on the process's working directory (and
// its tests run from any directory). The storefront's ru.yaml/ky.yaml
// are still read from disk by internal/web via i18n.Load("locales").
package locales

import (
	"embed"
	"sync"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// Admin holds admin.ru.yaml and admin.ky.yaml — load with
// i18n.LoadFS(locales.Admin, "admin.%s.yaml").
//
//go:embed admin.ru.yaml admin.ky.yaml
var Admin embed.FS

// Errors holds errors.ru.yaml and errors.ky.yaml — the client-facing
// texts of internal/apperr error codes ("err.<code>[.<variant>]") and of
// the product import report ("import.*"). Load with
// i18n.LoadFS(locales.Errors, "errors.%s.yaml"); internal/apperr does.
//
//go:embed errors.ru.yaml errors.ky.yaml
var Errors embed.FS

// AdminBundle returns the admin panel's translation set, loaded once —
// for every package that renders admin-facing text (internal/admin's
// pages, the Excel exports and the import template). A broken embedded
// file is a build-time mistake, so it panics.
var AdminBundle = sync.OnceValue(func() *i18n.Bundle {
	b, err := i18n.LoadFS(Admin, "admin.%s.yaml")
	if err != nil {
		panic(err)
	}
	return b
})
