//go:build integration

package integration

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/media"
)

// memStore is an in-memory stand-in for MinIO. Unknown keys are an error,
// like a missing object.
type memStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (s *memStore) GetObject(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("no such object %s", key)
	}
	return data, nil
}

func (s *memStore) PutObject(_ context.Context, key string, data []byte, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = data
	return nil
}

func jpegOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// framedPhoto is a 1200×1200 photo as saved before 23.11.2026: a red
// shoe filling the inner square, inside a 96 px (8%) white frame.
func framedPhoto(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1200, 1200))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(96, 96, 1104, 1104), image.NewUniform(color.RGBA{R: 220, A: 255}), image.Point{}, draw.Src)
	return jpegOf(t, img)
}

// unframedPhoto is a photo uploaded after the change: it fills the square.
func unframedPhoto(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1200, 1200))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{B: 220, A: 255}), image.Point{}, draw.Src)
	return jpegOf(t, img)
}

func legacyMark(t *testing.T, id string) bool {
	t.Helper()
	var marked bool
	if err := testDB.QueryRowContext(ctxT(t), `SELECT legacy_frame FROM product_images WHERE id = $1`, id).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	return marked
}

func photoKey(t *testing.T, id string) string {
	t.Helper()
	var key string
	if err := testDB.QueryRowContext(ctxT(t), `SELECT object_key FROM product_images WHERE id = $1`, id).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}

// TestReprocessLegacyPhotos: a marked (legacy_frame) photo with a white
// frame gets new full/thumb objects without it and its row points at
// them; a marked photo without a frame is left alone; an UNMARKED photo —
// even one that looks exactly like a framed one (a new upload with a white
// studio background) — is never touched; a dry run writes nothing; a
// second run finds nothing to do.
func TestReprocessLegacyPhotos(t *testing.T) {
	ctx := ctxT(t)
	f := newFixture(t, 1, 1)
	tag := strings.ReplaceAll(f.ProductID, "-", "")[:12]
	framedKey := "products/it-framed-" + tag + "/full.jpg"
	plainKey := "products/it-plain-" + tag + "/full.jpg"
	studioKey := "products/it-studio-" + tag + "/full.jpg"
	store := &memStore{objects: map[string][]byte{
		framedKey: framedPhoto(t),
		plainKey:  unframedPhoto(t),
		studioKey: framedPhoto(t),
	}}

	var framedID, plainID, studioID string
	for _, row := range []struct {
		dst    *string
		key    string
		marked bool
	}{{&framedID, framedKey, true}, {&plainID, plainKey, true}, {&studioID, studioKey, false}} {
		if err := testDB.QueryRowContext(ctx,
			`INSERT INTO product_images (product_id, object_key, legacy_frame) VALUES ($1, $2, $3) RETURNING id`,
			f.ProductID, row.key, row.marked).Scan(row.dst); err != nil {
			t.Fatal(err)
		}
	}

	// Other tests' rows share this database and their objects are not in
	// memStore — they show up as failures, so only our rows are checked.
	if _, err := media.ReprocessLegacyPhotos(ctx, testDB, store, true, io.Discard); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if got := photoKey(t, framedID); got != framedKey {
		t.Fatalf("dry run changed the row: %s", got)
	}
	if len(store.objects) != 3 {
		t.Fatalf("dry run wrote objects: %d in store", len(store.objects))
	}
	if marked := legacyMark(t, framedID); !marked {
		t.Fatal("dry run cleared the mark")
	}

	if _, err := media.ReprocessLegacyPhotos(ctx, testDB, store, false, io.Discard); err != nil {
		t.Fatalf("apply: %v", err)
	}
	newKey := photoKey(t, framedID)
	if newKey == framedKey || !strings.HasPrefix(newKey, "products/") || !strings.HasSuffix(newKey, "/full.jpg") {
		t.Fatalf("framed photo row = %q, want a fresh products/<uuid>/full.jpg key", newKey)
	}
	if _, ok := store.objects[media.ThumbKey(newKey)]; !ok {
		t.Fatalf("thumb %s was not written", media.ThumbKey(newKey))
	}
	full, err := jpeg.Decode(bytes.NewReader(store.objects[newKey]))
	if err != nil {
		t.Fatal(err)
	}
	if r, _, _, _ := full.At(5, 5).RGBA(); r>>8 < 180 {
		t.Fatalf("corner of the reframed photo is not the shoe: r=%d", r>>8)
	}
	if _, ok := store.objects[framedKey]; !ok {
		t.Fatal("the old object must be kept")
	}
	if got := photoKey(t, plainID); got != plainKey {
		t.Fatalf("unframed photo was touched: %s", got)
	}
	if legacyMark(t, framedID) || legacyMark(t, plainID) {
		t.Fatal("the mark must be cleared once a photo is processed")
	}
	if got := photoKey(t, studioID); got != studioKey || legacyMark(t, studioID) {
		t.Fatalf("an unmarked photo was touched: key %s", got)
	}

	before := len(store.objects)
	if _, err := media.ReprocessLegacyPhotos(ctx, testDB, store, false, io.Discard); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := photoKey(t, framedID); got != newKey || len(store.objects) != before {
		t.Fatalf("second run reprocessed again: key %s, objects %d → %d", got, before, len(store.objects))
	}
}
