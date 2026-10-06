package media

import (
	"context"
	"image"
	"strings"
	"testing"
)

func TestNormalizeCategoryImage(t *testing.T) {
	transparent := image.NewRGBA(image.Rect(0, 0, 500, 400)) // all alpha 0
	cases := []struct {
		name     string
		data     []byte
		wantType string
		wantW    int
		wantH    int
	}{
		{"opaque photo becomes jpeg, not upscaled", encodePNG(t, solid(600, 300, red)), "image/jpeg", 600, 300},
		{"cut-out shoe stays transparent png", encodePNG(t, transparent), "image/png", 500, 400},
		{"large photo fits the tile box keeping aspect", encodeTestJPEG(t, solid(3200, 1600, red)), "image/jpeg", categoryImageMaxSide, categoryImageMaxSide / 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := normalizeCategoryImage(c.data)
			if err != nil {
				t.Fatal(err)
			}
			if out.ContentType != c.wantType {
				t.Errorf("content type = %s, want %s", out.ContentType, c.wantType)
			}
			b := decodeAny(t, out.Data).Bounds()
			if b.Dx() != c.wantW || b.Dy() != c.wantH {
				t.Errorf("size = %dx%d, want %dx%d", b.Dx(), b.Dy(), c.wantW, c.wantH)
			}
		})
	}
}

func TestNormalizeCategoryImageRejectsBadInput(t *testing.T) {
	_, err := normalizeCategoryImage(encodePNG(t, solid(1000, 199, red)))
	assertBadRequest(t, err, "image_too_small")

	_, err = normalizeCategoryImage([]byte("not an image"))
	assertBadRequest(t, err, "unsupported_image")
}

func TestStoreCategoryImageUsesFreshCategoryKey(t *testing.T) {
	store := newFakeStore()
	data := encodeTestJPEG(t, solid(400, 400, red))

	key1, err := storeCategoryImage(context.Background(), store, data)
	if err != nil {
		t.Fatal(err)
	}
	key2, err := storeCategoryImage(context.Background(), store, data)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(key1, "categories/") || !strings.HasSuffix(key1, ".jpg") || key1 == key2 {
		t.Errorf("keys = %q, %q", key1, key2)
	}
	if store.contentTypes[key1] != "image/jpeg" || len(store.objects) != 2 {
		t.Errorf("stored = %v", store.contentTypes)
	}
}

func TestStoreCategoryImageRejectsOversizedFile(t *testing.T) {
	store := newFakeStore()
	_, err := storeCategoryImage(context.Background(), store, make([]byte, MaxUploadFileBytes+1))
	assertBadRequest(t, err, "file_too_large")
	if len(store.objects) != 0 {
		t.Errorf("stored %d objects for a rejected file", len(store.objects))
	}
}
