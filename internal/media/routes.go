package media

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// presignUploadTTL bounds how long a presigned upload URL stays valid: long
// enough for an admin to actually pick a file and let it upload, short
// enough to limit how long an unused URL (e.g. leaked in a log or browser
// history) could be used to write to the bucket.
const presignUploadTTL = 10 * time.Minute

// RegisterRoutes mounts the media upload endpoints under /admin/api/*,
// gated by staffSvc's RBAC.
func RegisterRoutes(mux *http.ServeMux, client *Client, staffSvc *staff.Service) {
	requireUploader := staffSvc.RequireRole(staff.RoleOwner, staff.RoleManager)

	mux.Handle("POST /admin/api/media/presign-upload", requireUploader(apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var req struct {
			ContentType string `json:"content_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return apperr.BadRequest("bad_request", "некорректное тело запроса")
		}

		objectKey, err := newObjectKey(req.ContentType)
		if err != nil {
			return err
		}

		uploadURL, err := client.PresignPut(r.Context(), objectKey, presignUploadTTL)
		if err != nil {
			return apperr.Internal(err)
		}

		return writeJSON(w, http.StatusOK, map[string]string{
			"upload_url": uploadURL,
			"object_key": objectKey,
		})
	})))
}

func writeJSON(w http.ResponseWriter, status int, body any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(body)
}
