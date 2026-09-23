package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/config"
)

// fakeStore is an in-memory objectStore. failPutSuffix makes PutObject
// fail for keys ending with it, to exercise the cleanup path.
type fakeStore struct {
	mu            sync.Mutex
	objects       map[string][]byte
	contentTypes  map[string]string
	removed       []string
	failPutSuffix string
}

func newFakeStore() *fakeStore {
	return &fakeStore{objects: map[string][]byte{}, contentTypes: map[string]string{}}
}

func (s *fakeStore) PutObject(_ context.Context, key string, data []byte, contentType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failPutSuffix != "" && strings.HasSuffix(key, s.failPutSuffix) {
		return errors.New("minio: connection refused")
	}
	s.objects[key] = data
	s.contentTypes[key] = contentType
	return nil
}

func (s *fakeStore) RemoveObject(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	s.removed = append(s.removed, key)
	return nil
}

func testConfig() *config.Config {
	return &config.Config{MinIOEndpoint: "localhost:9000", MinIOBucket: "cozy-media"}
}

// multipartBody builds a multipart/form-data body with one file part.
func multipartBody(t *testing.T, field string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, "photo.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func doUpload(t *testing.T, store objectStore, body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/api/media/upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	apperr.Wrap(uploadHandler(store, testConfig())).ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %q", rec.Body.String())
	}
	return body.Code
}

func TestUploadStoresBothVariants(t *testing.T) {
	store := newFakeStore()
	body, ct := multipartBody(t, "file", encodePNG(t, solid(1000, 700, red)))

	rec := doUpload(t, store, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	var resp uploadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.ObjectKey, "products/") || !strings.HasSuffix(resp.ObjectKey, "/full.jpg") {
		t.Fatalf("object_key = %q, want products/<uuid>/full.jpg", resp.ObjectKey)
	}
	thumbKey := ThumbKey(resp.ObjectKey)
	if resp.URL != "http://localhost:9000/cozy-media/"+resp.ObjectKey {
		t.Errorf("url = %q", resp.URL)
	}
	if resp.ThumbURL != "http://localhost:9000/cozy-media/"+thumbKey {
		t.Errorf("thumb_url = %q", resp.ThumbURL)
	}

	if len(store.objects) != 2 {
		t.Fatalf("stored %d objects, want 2: %v", len(store.objects), store.objects)
	}
	for key, want := range map[string]int{resp.ObjectKey: fullSize, thumbKey: thumbSize} {
		data, ok := store.objects[key]
		if !ok {
			t.Fatalf("object %q not stored", key)
		}
		if store.contentTypes[key] != "image/jpeg" {
			t.Errorf("%s content type = %q", key, store.contentTypes[key])
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || format != "jpeg" || cfg.Width != want || cfg.Height != want {
			t.Errorf("%s: %s %dx%d (err %v), want jpeg %dx%d", key, format, cfg.Width, cfg.Height, err, want, want)
		}
	}
}

func TestUploadRejectsBodyOver10MB(t *testing.T) {
	store := newFakeStore()
	body, ct := multipartBody(t, "file", bytes.Repeat([]byte{0x42}, maxUploadFileBytes+multipartOverhead+1))

	rec := doUpload(t, store, body, ct)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "file_too_large" {
		t.Errorf("code = %q, want file_too_large", code)
	}
	if len(store.objects) != 0 {
		t.Errorf("stored objects on a rejected upload: %v", store.objects)
	}
}

func TestUploadRejectsFileJustOver10MBWithinEnvelopeSlack(t *testing.T) {
	// The body fits under MaxBytesReader thanks to multipartOverhead, but
	// the file itself is one byte over the limit.
	body, ct := multipartBody(t, "file", bytes.Repeat([]byte{0x42}, maxUploadFileBytes+1))
	rec := doUpload(t, newFakeStore(), body, ct)
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "file_too_large" {
		t.Fatalf("status = %d body %s, want 400 file_too_large", rec.Code, rec.Body.String())
	}
}

func TestUploadRejectsBadRequests(t *testing.T) {
	t.Run("not multipart", func(t *testing.T) {
		rec := doUpload(t, newFakeStore(), bytes.NewBufferString(`{"content_type":"image/png"}`), "application/json")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
	t.Run("no file field", func(t *testing.T) {
		body, ct := multipartBody(t, "photo", encodePNG(t, solid(800, 800, red)))
		rec := doUpload(t, newFakeStore(), body, ct)
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "file_required" {
			t.Fatalf("status = %d body %s, want 400 file_required", rec.Code, rec.Body.String())
		}
	})
	t.Run("not an image", func(t *testing.T) {
		body, ct := multipartBody(t, "file", []byte("MZ\x90\x00 definitely an exe"))
		rec := doUpload(t, newFakeStore(), body, ct)
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "unsupported_image" {
			t.Fatalf("status = %d body %s, want 400 unsupported_image", rec.Code, rec.Body.String())
		}
	})
	t.Run("too small", func(t *testing.T) {
		body, ct := multipartBody(t, "file", encodePNG(t, solid(500, 500, red)))
		rec := doUpload(t, newFakeStore(), body, ct)
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "image_too_small" {
			t.Fatalf("status = %d body %s, want 400 image_too_small", rec.Code, rec.Body.String())
		}
	})
}

func TestUploadRemovesFullWhenThumbFails(t *testing.T) {
	store := newFakeStore()
	store.failPutSuffix = thumbSuffix
	body, ct := multipartBody(t, "file", encodePNG(t, solid(800, 800, red)))

	rec := doUpload(t, store, body, ct)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if len(store.objects) != 0 {
		t.Errorf("objects left behind after a failed upload: %v", store.objects)
	}
	if len(store.removed) != 1 || !strings.HasSuffix(store.removed[0], fullSuffix) {
		t.Errorf("removed = %v, want the full variant", store.removed)
	}
}

func TestUploadFullFailureStoresNothing(t *testing.T) {
	store := newFakeStore()
	store.failPutSuffix = fullSuffix
	body, ct := multipartBody(t, "file", encodePNG(t, solid(800, 800, red)))

	rec := doUpload(t, store, body, ct)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if len(store.objects) != 0 || len(store.removed) != 0 {
		t.Errorf("objects = %v, removed = %v; want nothing written or removed", store.objects, store.removed)
	}
}
