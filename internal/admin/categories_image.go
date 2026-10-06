// categories_image.go is the photo half of the Категории screen
// (feat/category-photo): the picture shown on the mobile app's catalog
// category tiles. It is uploaded with the create/edit modal's multipart
// form and goes through internal/media's pipeline exactly like the home
// banner's pictures (banner_page.go): the file limit, decoding-based type
// check, normalization and a server-generated key (categories/<uuid>.ext).
//
// Object lifecycle, same as banner and product photos: a photo uploaded
// for a save that then fails is removed again; a replaced or removed
// photo, and the photo of a deleted category, stay in the bucket — a
// page or app response cached a minute ago (GET /api/v1/categories is
// publicly cacheable) may still point at them.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/media"
)

const (
	// categoryFormMaxBytes caps the whole multipart body: one photo at the
	// media limit plus the text fields.
	categoryFormMaxBytes = media.MaxUploadFileBytes + 1<<20
	// categoryImageField / categoryRemoveField are the form's photo inputs.
	categoryImageField  = "image"
	categoryRemoveField = "remove_image"
)

// categoryImageStore uploads category photos; *media.Client in production.
type categoryImageStore interface {
	StoreCategoryImage(ctx context.Context, data []byte) (string, error)
	RemoveObject(ctx context.Context, objectKey string) error
}

// parsedCategoryForm releases the temp files of a parsed multipart form.
type parsedCategoryForm struct{ r *http.Request }

func (f parsedCategoryForm) cleanup() {
	if f.r.MultipartForm != nil {
		_ = f.r.MultipartForm.RemoveAll()
	}
}

// parseCategoryForm parses the create/edit form — multipart from the
// screen, plain urlencoded still accepted — under categoryFormMaxBytes.
// On failure it renders the screen with the error and returns false.
func (h *handlers) parseCategoryForm(w http.ResponseWriter, r *http.Request) (parsedCategoryForm, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, categoryFormMaxBytes)
	if err := r.ParseMultipartForm(bannerFormMemory); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		form := parsedCategoryForm{r: r}
		form.cleanup()
		h.renderCategoriesPage(w, r, uploadParseError(h.tr(r), err))
		return form, false
	}
	return parsedCategoryForm{r: r}, true
}

// uploadCategoryImage stores the form's "image" file as a category photo
// and returns its key; "" when no file was chosen.
func (h *handlers) uploadCategoryImage(ctx context.Context, r *http.Request) (string, error) {
	data, err := readUploadFile(r, categoryImageField)
	if err != nil || data == nil {
		return "", err
	}
	return h.categoryImages.StoreCategoryImage(ctx, data)
}

// categoryImageErr is the admin-facing text of a photo upload/save error,
// logging anything that isn't an expected apperr (bad file, too large).
func (h *handlers) categoryImageErr(ctx context.Context, t tr, err error) string {
	if !isAppErr(err) {
		slog.ErrorContext(ctx, "admin: saving category photo failed", "err", err)
	}
	return localizedErrMessage(t, err)
}

// removeCategoryUpload deletes a freshly uploaded photo of a failed save.
// Best effort: a leftover object only costs storage.
func (h *handlers) removeCategoryUpload(ctx context.Context, key string) {
	if key == "" {
		return
	}
	if err := h.categoryImages.RemoveObject(context.WithoutCancel(ctx), key); err != nil {
		slog.ErrorContext(ctx, "admin: removing orphaned category photo failed", "key", key, "err", err)
	}
}

// withCategoryImageChange returns a copy of e that also journals the
// photo change from → to (object keys; nil = no photo). An unchanged
// photo adds nothing. e itself is not modified.
func withCategoryImageChange(e audit.Entry, from, to *string) audit.Entry {
	if deref(from) == deref(to) {
		return e
	}
	details := make(map[string]any, len(e.Details)+1)
	for k, v := range e.Details {
		details[k] = v
	}
	details["image"] = audit.Change{From: deref(from), To: deref(to)}
	out := e
	out.Details = details
	return out
}
