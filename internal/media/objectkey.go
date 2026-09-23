package media

import (
	"strings"

	"github.com/google/uuid"
)

// Object key layout for product photos.
//
// Every photo uploaded through POST /admin/api/media/upload is stored as
// two normalized variants under one server-generated prefix:
//
//	products/<uuid>/full.jpg   — 1200×1200, product screen
//	products/<uuid>/thumb.jpg  —  400×400,  grids/lists
//
// product_images.object_key stores only the full key; the thumb key is
// derived from it by ThumbKey. Keys uploaded before this layout existed
// (products/<uuid>.<ext>, a single un-normalized file) stay valid and
// ThumbKey returns them unchanged, so they keep being served as is.
// The same rule is documented in openapi.yaml for the mobile app.
const (
	fullSuffix  = "/full.jpg"
	thumbSuffix = "/thumb.jpg"
)

// newVariantKeys generates a fresh, server-controlled pair of object keys
// for one upload. The caller never supplies (or influences) any part of
// the key, so an upload can't be used to pick an arbitrary path, escape
// the prefix, or overwrite an existing object.
func newVariantKeys() (fullKey, thumbKey string) {
	prefix := "products/" + uuid.NewString()
	return prefix + fullSuffix, prefix + thumbSuffix
}

// ThumbKey returns the object key of the 400px thumb variant for a
// product photo's (full) object key: "…/full.jpg" becomes "…/thumb.jpg".
// Any other key — a legacy single-file upload, or "" for "no photo" — is
// returned unchanged, so callers can use it unconditionally wherever a
// grid/list thumbnail URL is built. This is the single place the
// full→thumb naming rule lives on the Go side; do not inline it.
func ThumbKey(objectKey string) string {
	if base, ok := strings.CutSuffix(objectKey, fullSuffix); ok {
		return base + thumbSuffix
	}
	return objectKey
}
