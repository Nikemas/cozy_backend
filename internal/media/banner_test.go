package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func decodeAny(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return img
}

func TestNormalizeBannerImage(t *testing.T) {
	transparent := image.NewRGBA(image.Rect(0, 0, 800, 400)) // all alpha 0
	cases := []struct {
		name     string
		data     []byte
		kind     BannerImageKind
		wantType string
		wantW    int
		wantH    int
	}{
		{"opaque picture becomes jpeg, not upscaled", encodePNG(t, solid(600, 300, red)), BannerPicture, "image/jpeg", 600, 300},
		{"transparent picture stays png", encodePNG(t, transparent), BannerPicture, "image/png", 800, 400},
		{"large picture fits 1200 box keeping aspect", encodeTestJPEG(t, solid(3000, 1500, red)), BannerPicture, "image/jpeg", 1200, 600},
		{"wide background fits 2400x1200 box", encodeTestJPEG(t, solid(4800, 1200, red)), BannerBackground, "image/jpeg", 2400, 600},
		{"transparent background is flattened to jpeg", encodePNG(t, transparent), BannerBackground, "image/jpeg", 800, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := normalizeBannerImage(c.data, c.kind)
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

func TestNormalizeBannerImageRejectsBadInput(t *testing.T) {
	_, err := normalizeBannerImage(encodePNG(t, solid(1000, 199, red)), BannerPicture)
	assertBadRequest(t, err, "image_too_small")

	_, err = normalizeBannerImage(encodePNG(t, solid(1600, 399, red)), BannerBackground)
	assertBadRequest(t, err, "image_too_small")

	_, err = normalizeBannerImage([]byte("not an image"), BannerPicture)
	assertBadRequest(t, err, "unsupported_image")
}

func TestStoreBannerImageUsesFreshBannerKey(t *testing.T) {
	store := newFakeStore()
	var buf bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 300, 300))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255}) // the rest stays transparent
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	key1, err := storeBannerImage(context.Background(), store, buf.Bytes(), BannerPicture)
	if err != nil {
		t.Fatal(err)
	}
	key2, err := storeBannerImage(context.Background(), store, buf.Bytes(), BannerPicture)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(key1, "banners/") || !strings.HasSuffix(key1, ".png") || key1 == key2 {
		t.Errorf("keys = %q, %q", key1, key2)
	}
	if store.contentTypes[key1] != "image/png" || len(store.objects) != 2 {
		t.Errorf("stored = %v", store.contentTypes)
	}
}

func TestStoreBannerImageRejectsOversizedFile(t *testing.T) {
	_, err := storeBannerImage(context.Background(), newFakeStore(), make([]byte, MaxUploadFileBytes+1), BannerBackground)
	assertBadRequest(t, err, "file_too_large")
}
