package media

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // register the PNG decoder for image.Decode
	"math"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register the WebP decoder for image.Decode

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// Normalization parameters for product photos (Wave 5 / Task R in
// tasks/plan.md). The numbers come from the mobile design system §4.2:
// the product photo sits in a white 1:1 well with BoxFit.contain, the
// shoe taking up ~88% of the scene.
const (
	// fullSize is the side of the `full` variant (product screen).
	fullSize = 1200
	// thumbSize is the side of the `thumb` variant (2-column grid on a
	// 3x screen: ~130pt tile ≈ 400px).
	thumbSize = 400
	// paddingRatio is the empty margin on every side, as a fraction of
	// the canvas side — the image is fitted into the inner
	// (1 - 2*paddingRatio) square.
	paddingRatio = 0.08
	// jpegQuality is the encoder quality for both variants.
	jpegQuality = 85

	// minSourceSide rejects sources whose shorter side is below it:
	// upscaling a small photo into the 1200px variant only produces mush.
	minSourceSide = 600
	// maxSourcePixels caps width*height before the full decode. A 10 MB
	// file can still declare e.g. a 30000×30000 PNG (a "decompression
	// bomb") that would need gigabytes of RAM once decoded; DecodeConfig
	// reads only the header, so the check is free. 50 MP covers every
	// phone/camera JPEG in practice.
	maxSourcePixels = 50_000_000
)

// acceptedFormats are the image.Decode format names we let through —
// the registered decoders (stdlib jpeg/png + x/image/webp) could in
// principle grow (e.g. another package registering gif), so the result
// is checked explicitly rather than trusting "it decoded".
var acceptedFormats = map[string]bool{"jpeg": true, "png": true, "webp": true}

// normalizedImage is the output of normalizeImage: both variants,
// JPEG-encoded, with no metadata (image/jpeg's encoder writes no EXIF/ICC
// segments, so nothing from the source file survives).
type normalizedImage struct {
	Full  []byte
	Thumb []byte
}

// normalizeImage turns an uploaded file into the two product-photo
// variants: the real format is detected by decoding (never from a
// client-supplied Content-Type), EXIF orientation is applied, and the
// result is fitted (contain) into a white square with paddingRatio
// margins — so every product photo in the catalog has the same geometry
// and background regardless of how it was shot. Transparent PNG/WebP
// pixels end up white too (composited over the white canvas).
//
// It is a pure function (bytes in, bytes out; no HTTP, no MinIO) so it
// can be unit tested on images generated in the test itself. Any problem
// with the input itself is returned as an apperr.BadRequest with a
// Russian message meant to be shown to the admin as is.
func normalizeImage(data []byte) (*normalizedImage, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !acceptedFormats[format] {
		return nil, errUnsupportedImage()
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxSourcePixels {
		return nil, apperr.BadRequest("image_too_large",
			fmt.Sprintf("слишком большое разрешение: %d×%d px (максимум %d Мп)", cfg.Width, cfg.Height, maxSourcePixels/1_000_000))
	}
	// Rotation by EXIF only swaps width and height, so the shorter side
	// is the same before and after — safe to check on the raw header.
	if min(cfg.Width, cfg.Height) < minSourceSide {
		return nil, apperr.BadRequest("image_too_small",
			fmt.Sprintf("фото слишком маленькое: %d×%d px — меньшая сторона должна быть не меньше %d px", cfg.Width, cfg.Height, minSourceSide))
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// Header parsed but the body is truncated/corrupt.
		return nil, errUnsupportedImage()
	}

	src = applyOrientation(src, exifOrientation(data))

	full := fitOnWhiteSquare(src, fullSize)
	// thumb is downscaled from the already-normalized full canvas: the
	// padding scales proportionally, so it is identical geometry for a
	// fraction of the work of resampling the multi-megapixel source again.
	thumb := image.NewRGBA(image.Rect(0, 0, thumbSize, thumbSize))
	draw.CatmullRom.Scale(thumb, thumb.Bounds(), full, full.Bounds(), draw.Src, nil)

	fullJPEG, err := encodeJPEG(full)
	if err != nil {
		return nil, err
	}
	thumbJPEG, err := encodeJPEG(thumb)
	if err != nil {
		return nil, err
	}
	return &normalizedImage{Full: fullJPEG, Thumb: thumbJPEG}, nil
}

func errUnsupportedImage() error {
	return apperr.BadRequest("unsupported_image", "файл не является изображением JPEG, PNG или WEBP")
}

// fitOnWhiteSquare returns a size×size opaque canvas filled with white,
// with src scaled (up or down, preserving aspect ratio) to fit inside the
// inner square left after paddingRatio margins, and centered. draw.Over
// composites any transparency in src onto the white background.
func fitOnWhiteSquare(src image.Image, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)

	sb := src.Bounds()
	inner := float64(size) * (1 - 2*paddingRatio)
	scale := math.Min(inner/float64(sb.Dx()), inner/float64(sb.Dy()))
	w := max(1, int(math.Round(float64(sb.Dx())*scale)))
	h := max(1, int(math.Round(float64(sb.Dy())*scale)))
	x0 := (size - w) / 2
	y0 := (size - h) / 2

	draw.CatmullRom.Scale(dst, image.Rect(x0, y0, x0+w, y0+h), src, sb, draw.Over, nil)
	return dst
}

func encodeJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, fmt.Errorf("encode jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

// applyOrientation returns src transformed so it displays upright, per
// the EXIF Orientation tag value o (1–8, see exif.go). o == 1 or any
// unknown value returns src unchanged. For the other seven values the
// pixels are copied into a new RGBA image (alpha preserved, so a
// transparent PNG still composites onto white later).
func applyOrientation(src image.Image, o int) image.Image {
	if o < 2 || o > 8 {
		return src
	}

	// Flatten to RGBA once (draw.Draw has fast paths for the YCbCr a
	// JPEG decodes to), then remap pixels by direct Pix indexing.
	b := src.Bounds()
	in := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(in, in.Bounds(), src, b.Min, draw.Src)
	w, h := b.Dx(), b.Dy()

	// Orientations 5–8 swap the axes.
	ow, oh := w, h
	if o >= 5 {
		ow, oh = h, w
	}
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2: // mirrored horizontally
				dx, dy = w-1-x, y
			case 3: // rotated 180°
				dx, dy = w-1-x, h-1-y
			case 4: // mirrored vertically
				dx, dy = x, h-1-y
			case 5: // transpose (mirror + 90° CCW)
				dx, dy = y, x
			case 6: // needs 90° CW rotation
				dx, dy = h-1-y, x
			case 7: // transverse (mirror + 90° CW)
				dx, dy = h-1-y, w-1-x
			case 8: // needs 90° CCW rotation
				dx, dy = y, w-1-x
			}
			si := in.PixOffset(x, y)
			di := out.PixOffset(dx, dy)
			copy(out.Pix[di:di+4], in.Pix[si:si+4])
		}
	}
	return out
}
