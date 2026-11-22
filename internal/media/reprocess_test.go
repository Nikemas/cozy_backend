package media

import (
	"image"
	"image/color"
	"math"
	"testing"

	"golang.org/x/image/draw"
)

// legacyFramed reproduces how photos were normalized before 23.11.2026:
// src fitted into the inner square of a white fullSize canvas with a
// legacyPaddingRatio margin.
func legacyFramed(src image.Image) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, fullSize, fullSize))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	sb := src.Bounds()
	inner := float64(fullSize) * (1 - 2*legacyPaddingRatio)
	scale := math.Min(inner/float64(sb.Dx()), inner/float64(sb.Dy()))
	w := int(math.Round(float64(sb.Dx()) * scale))
	h := int(math.Round(float64(sb.Dy()) * scale))
	x0, y0 := (fullSize-w)/2, (fullSize-h)/2
	draw.CatmullRom.Scale(dst, image.Rect(x0, y0, x0+w, y0+h), src, sb, draw.Over, nil)
	return dst
}

func TestReframeLegacySquarePhotoFillsTheCanvas(t *testing.T) {
	legacy := encodeTestJPEG(t, legacyFramed(solid(1000, 1000, red)))

	out, ok, err := reframeLegacyPhoto(legacy)
	if err != nil || !ok {
		t.Fatalf("reframeLegacyPhoto: ok=%v err=%v, want ok", ok, err)
	}
	full := decodeJPEG(t, out.Full)
	thumb := decodeJPEG(t, out.Thumb)
	if full.Bounds().Dx() != fullSize || thumb.Bounds().Dx() != thumbSize {
		t.Fatalf("sizes = %v / %v", full.Bounds().Size(), thumb.Bounds().Size())
	}
	assertPixel(t, full, 4, 4, red, "top-left corner, was white frame")
	assertPixel(t, full, 1195, 600, red, "right edge, was white frame")
	assertPixel(t, thumb, 2, 2, red, "thumb corner")
}

func TestReframeLegacyWidePhotoKeepsTheWholeShoe(t *testing.T) {
	// A 2:1 shoe shot: after reframing it spans the full width, with white
	// letterbox above and below — the same as a fresh upload.
	legacy := encodeTestJPEG(t, legacyFramed(solid(1600, 800, blue)))

	out, ok, err := reframeLegacyPhoto(legacy)
	if err != nil || !ok {
		t.Fatalf("reframeLegacyPhoto: ok=%v err=%v, want ok", ok, err)
	}
	full := decodeJPEG(t, out.Full)
	assertPixel(t, full, 4, 600, blue, "left edge reaches the canvas edge")
	assertPixel(t, full, 1195, 600, blue, "right edge reaches the canvas edge")
	assertPixel(t, full, 600, 250, white, "letterbox above")
	assertPixel(t, full, 600, 950, white, "letterbox below")
}

func TestReframeLeavesPhotosWithoutTheFrameAlone(t *testing.T) {
	tests := []struct {
		name string
		img  image.Image
	}{
		{"new upload filling the square", solid(fullSize, fullSize, red)},
		{"new wide upload touching left and right", func() image.Image {
			img := solid(fullSize, fullSize, white)
			draw.Draw(img, image.Rect(0, 300, fullSize, 900), image.NewUniform(blue), image.Point{}, draw.Src)
			return img
		}()},
		{"not the full size", solid(800, 800, white)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, ok, err := reframeLegacyPhoto(encodeTestJPEG(t, tt.img))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok || out != nil {
				t.Fatalf("reframed a photo without the legacy frame")
			}
		})
	}
}

func TestReframeIsIdempotent(t *testing.T) {
	legacy := encodeTestJPEG(t, legacyFramed(solid(1000, 1000, red)))
	once, ok, err := reframeLegacyPhoto(legacy)
	if err != nil || !ok {
		t.Fatalf("first pass: ok=%v err=%v", ok, err)
	}
	if _, ok, err := reframeLegacyPhoto(once.Full); err != nil || ok {
		t.Fatalf("second pass reframed again: ok=%v err=%v", ok, err)
	}
}

func TestReframeRejectsNonJPEG(t *testing.T) {
	if _, _, err := reframeLegacyPhoto([]byte("not an image")); err == nil {
		t.Fatal("want an error for a non-JPEG object")
	}
}
