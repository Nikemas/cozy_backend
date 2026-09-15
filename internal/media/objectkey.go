package media

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// allowedContentTypes maps a client-supplied image content type to the file
// extension used for the object key generated for it. Only real image MIME
// types are accepted — anything else (e.g. "text/html", "application/zip")
// is rejected before it ever reaches MinIO.
var allowedContentTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

// extensionForContentType validates contentType against the allow-list of
// image MIME types and returns the file extension to use for it. It is a
// pure function so the validation/mapping logic can be unit tested without
// a live MinIO server.
func extensionForContentType(contentType string) (string, error) {
	ext, ok := allowedContentTypes[contentType]
	if !ok {
		return "", apperr.BadRequest("unsupported_content_type", "неподдерживаемый тип файла: "+contentType)
	}
	return ext, nil
}

// newObjectKey generates a fresh, server-controlled MinIO object key for an
// upload of the given content type: a random UUID plus the extension that
// matches it, under a fixed "products/" prefix. The caller never supplies
// (or influences) the key itself — only the content type, which is
// validated first — so a presign request can't be used to pick an
// arbitrary path, escape the prefix, or overwrite an existing object.
func newObjectKey(contentType string) (string, error) {
	ext, err := extensionForContentType(contentType)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("products/%s%s", uuid.NewString(), ext), nil
}
