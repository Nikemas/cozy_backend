package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

const (
	// maxUploadFileBytes is the largest photo file the upload endpoint
	// accepts — well above any phone JPEG, small enough that buffering it
	// in memory for decoding is harmless.
	maxUploadFileBytes = 10 << 20
	// multipartOverhead is the slack on top of maxUploadFileBytes for the
	// multipart envelope (boundaries, part headers), so a file of exactly
	// 10 MB isn't rejected because of its wrapping.
	multipartOverhead = 64 << 10
	// uploadFormField is the multipart field name carrying the file.
	uploadFormField = "file"
)

// objectStore is the slice of *Client the upload handler needs — an
// interface so the handler can be tested with an in-memory fake instead
// of a live MinIO.
type objectStore interface {
	PutObject(ctx context.Context, objectKey string, data []byte, contentType string) error
	RemoveObject(ctx context.Context, objectKey string) error
}

// RegisterRoutes mounts the media upload endpoint under /admin/api/*,
// gated by staffSvc's RBAC. cfg builds the public URLs returned for the
// admin form's preview.
func RegisterRoutes(mux *http.ServeMux, client *Client, staffSvc *staff.Service, cfg *config.Config) {
	requireUploader := staffSvc.RequireRole(staff.RoleOwner, staff.RoleManager)
	mux.Handle("POST /admin/api/media/upload", requireUploader(apperr.Wrap(uploadHandler(client, cfg))))
}

// uploadResponse is the body of a successful upload. ObjectKey (the full
// variant's key) is what the caller stores in product_images; URL and
// ThumbURL are the public URLs of both variants, for an immediate preview
// of the normalized result.
type uploadResponse struct {
	ObjectKey string `json:"object_key"`
	URL       string `json:"url"`
	ThumbURL  string `json:"thumb_url"`
}

// uploadHandler serves POST /admin/api/media/upload: reads one image from
// the "file" multipart field (at most maxUploadFileBytes), normalizes it
// (normalizeImage) and stores both variants (storeVariants).
//
// The body is read as a stream via MultipartReader rather than
// ParseMultipartForm, so nothing spills to temp files and the only copy
// in memory is the (size-capped) file itself.
func uploadHandler(store objectStore, cfg *config.Config) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadFileBytes+multipartOverhead)

		data, err := readUploadFile(r)
		if err != nil {
			return err
		}

		img, err := normalizeImage(data)
		if err != nil {
			return err
		}

		fullKey, err := storeVariants(r.Context(), store, img)
		if err != nil {
			return err
		}

		return writeJSON(w, http.StatusOK, uploadResponse{
			ObjectKey: fullKey,
			URL:       cfg.PublicObjectURL(fullKey),
			ThumbURL:  cfg.PublicObjectURL(ThumbKey(fullKey)),
		})
	}
}

func errFileTooLarge() error {
	return apperr.BadRequest("file_too_large", "файл больше 10 МБ")
}

// readUploadFile returns the bytes of the uploadFormField part of a
// multipart request. Every client-side problem — not multipart, no file
// part, body over the MaxBytesReader limit — is a 400.
func readUploadFile(r *http.Request) ([]byte, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, apperr.BadRequest("bad_request", "ожидается multipart/form-data с полем file")
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, apperr.BadRequest("file_required", "файл не передан (поле file)")
		}
		if err != nil {
			return nil, uploadReadError(err)
		}
		if part.FormName() != uploadFormField {
			_ = part.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, maxUploadFileBytes+1))
		if err != nil {
			return nil, uploadReadError(err)
		}
		if len(data) > maxUploadFileBytes {
			return nil, errFileTooLarge()
		}
		return data, nil
	}
}

// uploadReadError maps an error from reading the request body to a 400:
// MaxBytesReader's limit error gets the "too large" message, anything
// else (truncated/malformed multipart) the generic one.
func uploadReadError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return errFileTooLarge()
	}
	return apperr.BadRequest("bad_request", "некорректное тело запроса")
}

// storeVariants uploads both variants of img under a fresh key pair and
// returns the full key. If the thumb upload fails, the already-uploaded
// full object is removed so a failed request never leaves half a photo
// behind (a full.jpg whose thumb.jpg 404s in every grid).
func storeVariants(ctx context.Context, store objectStore, img *normalizedImage) (string, error) {
	fullKey, thumbKey := newVariantKeys()

	if err := store.PutObject(ctx, fullKey, img.Full, "image/jpeg"); err != nil {
		return "", fmt.Errorf("put %s: %w", fullKey, err)
	}
	if err := store.PutObject(ctx, thumbKey, img.Thumb, "image/jpeg"); err != nil {
		// The request context may be what failed (client gone); cleanup
		// should still run.
		if rmErr := store.RemoveObject(context.WithoutCancel(ctx), fullKey); rmErr != nil {
			slog.Error("media: failed to remove orphaned full variant", "key", fullKey, "err", rmErr)
		}
		return "", fmt.Errorf("put %s: %w", thumbKey, err)
	}
	return fullKey, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(body)
}
