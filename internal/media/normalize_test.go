package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

var (
	red  = color.RGBA{R: 220, A: 255}
	blue = color.RGBA{B: 220, A: 255}
)

// solid returns a w×h image filled with c.
func solid(w, h int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// leftRightSplit returns a w×h image whose left half is left and right
// half is right — asymmetric, so a rotation is detectable in the output.
func leftRightSplit(w, h int, left, right color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				img.Set(x, y, left)
			} else {
				img.Set(x, y, right)
			}
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeTestJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tiffWithOrientation builds a minimal TIFF block holding just IFD0 with
// one Orientation entry, in the given byte order.
func tiffWithOrientation(bo binary.ByteOrder, o uint16) []byte {
	b := make([]byte, 8+2+12+4)
	if bo == binary.LittleEndian {
		copy(b, "II")
	} else {
		copy(b, "MM")
	}
	bo.PutUint16(b[2:], 42)
	bo.PutUint32(b[4:], 8) // IFD0 offset
	bo.PutUint16(b[8:], 1) // one entry
	bo.PutUint16(b[10:], 0x0112)
	bo.PutUint16(b[12:], 3) // SHORT
	bo.PutUint32(b[14:], 1) // count
	bo.PutUint16(b[18:], o)
	return b // next-IFD offset (last 4 bytes) stays 0
}

// withJPEGExif inserts an Exif APP1 segment right after the SOI marker,
// the way cameras/phones write it.
func withJPEGExif(jpg []byte, tiff []byte) []byte {
	payload := append(append([]byte{}, exifHeader...), tiff...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	seg = append(seg, payload...)
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

// withPNGChunk inserts a chunk right after IHDR (8-byte signature +
// 25-byte IHDR chunk), with a valid CRC so the PNG still decodes.
func withPNGChunk(pngData []byte, typ string, payload []byte) []byte {
	chunk := make([]byte, 4, 12+len(payload))
	binary.BigEndian.PutUint32(chunk, uint32(len(payload)))
	chunk = append(chunk, typ...)
	chunk = append(chunk, payload...)
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(chunk[4:]))
	chunk = append(chunk, crc...)
	const afterIHDR = 8 + 25
	out := append([]byte{}, pngData[:afterIHDR]...)
	out = append(out, chunk...)
	return append(out, pngData[afterIHDR:]...)
}

func decodeJPEG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("output format = %q, want jpeg", format)
	}
	return img
}

// near reports whether c is within tol of want on every RGB channel —
// JPEG is lossy, so exact comparisons would be flaky.
func near(c color.Color, want color.RGBA, tol int) bool {
	r, g, b, _ := c.RGBA()
	d := func(a uint32, w uint8) bool {
		diff := int(a>>8) - int(w)
		return diff <= tol && diff >= -tol
	}
	return d(r, want.R) && d(g, want.G) && d(b, want.B)
}

var white = color.RGBA{R: 255, G: 255, B: 255, A: 255}

// assertPixel checks the output pixel at (x, y) is close to want.
func assertPixel(t *testing.T, img image.Image, x, y int, want color.RGBA, what string) {
	t.Helper()
	if got := img.At(x, y); !near(got, want, 24) {
		r, g, b, _ := got.RGBA()
		t.Errorf("%s: pixel (%d,%d) = rgb(%d,%d,%d), want ≈ rgb(%d,%d,%d)", what, x, y, r>>8, g>>8, b>>8, want.R, want.G, want.B)
	}
}

func mustNormalize(t *testing.T, data []byte) (full, thumb image.Image) {
	t.Helper()
	out, err := normalizeImage(data)
	if err != nil {
		t.Fatalf("normalizeImage: unexpected error: %v", err)
	}
	full = decodeJPEG(t, out.Full)
	thumb = decodeJPEG(t, out.Thumb)
	if b := full.Bounds(); b.Dx() != fullSize || b.Dy() != fullSize {
		t.Errorf("full size = %v, want %dx%d", b.Size(), fullSize, fullSize)
	}
	if b := thumb.Bounds(); b.Dx() != thumbSize || b.Dy() != thumbSize {
		t.Errorf("thumb size = %v, want %dx%d", b.Size(), thumbSize, thumbSize)
	}
	return full, thumb
}

// Padding is 8% of 1200 = 96 px on every side, so the content box is
// [96, 1104). A 2:1 source fits 1008×504 → rows [348, 852).

func TestNormalizeWideImage(t *testing.T) {
	full, thumb := mustNormalize(t, encodePNG(t, solid(1600, 800, red)))

	assertPixel(t, full, 600, 600, red, "center")
	assertPixel(t, full, 110, 600, red, "left edge of content, inside padding box")
	assertPixel(t, full, 40, 600, white, "left padding")
	assertPixel(t, full, 600, 300, white, "above content (letterbox)")
	assertPixel(t, full, 600, 900, white, "below content (letterbox)")
	assertPixel(t, full, 5, 5, white, "corner")

	assertPixel(t, thumb, 200, 200, red, "thumb center")
	assertPixel(t, thumb, 200, 100, white, "thumb letterbox")
}

func TestNormalizeTallImage(t *testing.T) {
	full, _ := mustNormalize(t, encodePNG(t, solid(800, 1600, blue)))

	assertPixel(t, full, 600, 600, blue, "center")
	assertPixel(t, full, 600, 110, blue, "top edge of content")
	assertPixel(t, full, 600, 40, white, "top padding")
	assertPixel(t, full, 300, 600, white, "left of content (pillarbox)")
	assertPixel(t, full, 900, 600, white, "right of content (pillarbox)")
}

func TestNormalizeSquareImage(t *testing.T) {
	full, _ := mustNormalize(t, encodeTestJPEG(t, solid(1000, 1000, red)))

	assertPixel(t, full, 110, 110, red, "just inside the padding box")
	assertPixel(t, full, 1090, 1090, red, "just inside the padding box (far corner)")
	assertPixel(t, full, 50, 600, white, "left padding")
	assertPixel(t, full, 1150, 600, white, "right padding")
	assertPixel(t, full, 600, 50, white, "top padding")
	assertPixel(t, full, 600, 1150, white, "bottom padding")
}

func TestNormalizeSmallButValidImageIsUpscaled(t *testing.T) {
	// Exactly the minimum: 600 px shorter side is accepted.
	full, _ := mustNormalize(t, encodePNG(t, solid(600, 600, red)))
	assertPixel(t, full, 600, 600, red, "center")
}

func TestNormalizeTransparentPNGGetsWhiteBackground(t *testing.T) {
	// Fully transparent 1000×1000 with an opaque red 500×500 center.
	img := image.NewNRGBA(image.Rect(0, 0, 1000, 1000))
	for y := 250; y < 750; y++ {
		for x := 250; x < 750; x++ {
			img.Set(x, y, red)
		}
	}
	full, _ := mustNormalize(t, encodePNG(t, img))

	assertPixel(t, full, 600, 600, red, "opaque center")
	// Source (100,100) is transparent; maps to canvas 96 + 100*1.008 ≈ 197.
	assertPixel(t, full, 200, 200, white, "transparent area inside content box")
	// Must not turn black (what dropping alpha without compositing gives).
	assertPixel(t, full, 1000, 1000, white, "transparent area, far corner")
}

func TestNormalizeAppliesEXIFOrientation(t *testing.T) {
	// Stored landscape 1200×800: left half red, right half blue.
	// Orientation 6 = "rotate 90° CW to display": the displayed image is
	// portrait 800×1200 with red on TOP and blue at the bottom.
	src := encodeTestJPEG(t, leftRightSplit(1200, 800, red, blue))

	t.Run("without EXIF stays landscape", func(t *testing.T) {
		full, _ := mustNormalize(t, src)
		assertPixel(t, full, 400, 600, red, "left half")
		assertPixel(t, full, 800, 600, blue, "right half")
		assertPixel(t, full, 150, 600, red, "wide content reaches the padding box")
	})

	for name, bo := range map[string]binary.ByteOrder{"big-endian": binary.BigEndian, "little-endian": binary.LittleEndian} {
		t.Run("orientation 6 "+name, func(t *testing.T) {
			full, _ := mustNormalize(t, withJPEGExif(src, tiffWithOrientation(bo, 6)))
			// Portrait content: 672×1008, x in [264, 936).
			assertPixel(t, full, 600, 350, red, "top half")
			assertPixel(t, full, 600, 850, blue, "bottom half")
			assertPixel(t, full, 150, 600, white, "pillarbox (proves it was rotated)")
		})
	}

	t.Run("orientation 3 flips", func(t *testing.T) {
		full, _ := mustNormalize(t, withJPEGExif(src, tiffWithOrientation(binary.BigEndian, 3)))
		assertPixel(t, full, 400, 600, blue, "left half after 180°")
		assertPixel(t, full, 800, 600, red, "right half after 180°")
	})
}

func TestNormalizeOutputHasNoMetadata(t *testing.T) {
	src := withJPEGExif(encodeTestJPEG(t, solid(800, 800, red)), tiffWithOrientation(binary.BigEndian, 1))
	out, err := normalizeImage(src)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"full": out.Full, "thumb": out.Thumb} {
		if bytes.Contains(data, []byte("Exif")) {
			t.Errorf("%s output still contains an Exif segment", name)
		}
		if exifOrientation(data) != 1 {
			t.Errorf("%s output carries an orientation tag", name)
		}
	}
}

func assertBadRequest(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error, got nil", wantCode)
	}
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected *apperr.AppError, got %T: %v", err, err)
	}
	if appErr.Status != 400 || appErr.Code != wantCode {
		t.Fatalf("got %d %q (%s), want 400 %q", appErr.Status, appErr.Code, appErr.Message, wantCode)
	}
}

func TestNormalizeRejectsTooSmall(t *testing.T) {
	// Shorter side 599 < 600, even though the longer side is big.
	_, err := normalizeImage(encodePNG(t, solid(1600, 599, red)))
	assertBadRequest(t, err, "image_too_small")
}

func TestNormalizeRejectsNonImages(t *testing.T) {
	var gifBuf bytes.Buffer
	if err := gif.Encode(&gifBuf, solid(800, 800, red), nil); err != nil {
		t.Fatal(err)
	}
	pngData := encodePNG(t, solid(800, 800, red))

	cases := map[string][]byte{
		"empty":           {},
		"text":            []byte("<html><script>alert(1)</script></html>"),
		"svg":             []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="800" height="800"></svg>`),
		"gif":             gifBuf.Bytes(),
		"truncated png":   pngData[:len(pngData)/2],
		"jpeg magic+junk": append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{0x42}, 64)...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeImage(data)
			assertBadRequest(t, err, "unsupported_image")
		})
	}
}

func TestNormalizeRejectsDecompressionBomb(t *testing.T) {
	// A valid PNG header declaring 20000×20000 (400 MP): rejected from the
	// header alone, before any pixel buffer is allocated.
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 20000)
	binary.BigEndian.PutUint32(ihdr[4:], 20000)
	ihdr[8], ihdr[9] = 8, 2 // 8-bit RGB
	chunk := append([]byte("IHDR"), ihdr...)
	lenBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBytes, 13)
	buf.Write(lenBytes)
	buf.Write(chunk)
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(chunk))
	buf.Write(crc)

	_, err := normalizeImage(buf.Bytes())
	assertBadRequest(t, err, "image_too_large")
}

func TestApplyOrientationMapsCorners(t *testing.T) {
	// 3×2 source; the stored (0,0) pixel is red, (1,0) is blue. Where
	// those two land per the EXIF spec pins down both the rotation and
	// the mirroring of every orientation value.
	src := solid(3, 2, white)
	src.Set(0, 0, red)
	src.Set(1, 0, blue)

	type pt struct{ x, y int }
	cases := []struct {
		o      int
		w, h   int
		redAt  pt
		blueAt pt
	}{
		{1, 3, 2, pt{0, 0}, pt{1, 0}},
		{2, 3, 2, pt{2, 0}, pt{1, 0}},
		{3, 3, 2, pt{2, 1}, pt{1, 1}},
		{4, 3, 2, pt{0, 1}, pt{1, 1}},
		{5, 2, 3, pt{0, 0}, pt{0, 1}},
		{6, 2, 3, pt{1, 0}, pt{1, 1}},
		{7, 2, 3, pt{1, 2}, pt{1, 1}},
		{8, 2, 3, pt{0, 2}, pt{0, 1}},
	}
	for _, c := range cases {
		out := applyOrientation(src, c.o)
		if b := out.Bounds(); b.Dx() != c.w || b.Dy() != c.h {
			t.Errorf("orientation %d: size %v, want %dx%d", c.o, b.Size(), c.w, c.h)
			continue
		}
		if !near(out.At(c.redAt.x, c.redAt.y), red, 0) {
			t.Errorf("orientation %d: red pixel not at %v", c.o, c.redAt)
		}
		if !near(out.At(c.blueAt.x, c.blueAt.y), blue, 0) {
			t.Errorf("orientation %d: blue pixel not at %v", c.o, c.blueAt)
		}
	}
}

func TestExifOrientationContainers(t *testing.T) {
	jpg := encodeTestJPEG(t, solid(8, 8, red))
	pngData := encodePNG(t, solid(8, 8, red))

	webp := func(exifPayload []byte) []byte {
		var b bytes.Buffer
		b.WriteString("RIFF\x00\x00\x00\x00WEBP")
		// An unrelated odd-sized chunk first, to exercise padding.
		b.WriteString("XTRA")
		_ = binary.Write(&b, binary.LittleEndian, uint32(3))
		b.WriteString("abc\x00")
		b.WriteString("EXIF")
		_ = binary.Write(&b, binary.LittleEndian, uint32(len(exifPayload)))
		b.Write(exifPayload)
		return b.Bytes()
	}

	cases := map[string]struct {
		data []byte
		want int
	}{
		"jpeg without exif":       {jpg, 1},
		"jpeg orientation 8":      {withJPEGExif(jpg, tiffWithOrientation(binary.LittleEndian, 8)), 8},
		"png eXIf orientation 6":  {withPNGChunk(pngData, "eXIf", tiffWithOrientation(binary.BigEndian, 6)), 6},
		"png without eXIf":        {pngData, 1},
		"webp EXIF orientation 3": {webp(tiffWithOrientation(binary.LittleEndian, 3)), 3},
		"webp Exif-prefixed":      {webp(append(append([]byte{}, exifHeader...), tiffWithOrientation(binary.BigEndian, 5)...)), 5},
		"out-of-range value":      {withJPEGExif(jpg, tiffWithOrientation(binary.BigEndian, 9)), 1},
		"garbage":                 {[]byte("not an image at all"), 1},
		"empty":                   {nil, 1},
	}
	for name, c := range cases {
		if got := exifOrientation(c.data); got != c.want {
			t.Errorf("%s: exifOrientation = %d, want %d", name, got, c.want)
		}
	}

	// The PNG with an eXIf chunk must still be a valid PNG.
	if _, err := png.Decode(bytes.NewReader(cases["png eXIf orientation 6"].data)); err != nil {
		t.Errorf("png with eXIf chunk no longer decodes: %v", err)
	}
}

func TestExifOrientationNeverPanicsOnTruncatedInput(t *testing.T) {
	full := withJPEGExif(encodeTestJPEG(t, solid(8, 8, red)), tiffWithOrientation(binary.BigEndian, 6))
	for n := 0; n <= len(full); n++ {
		_ = exifOrientation(full[:n]) // must not panic
	}
	// Corrupt IFD offset / entry count pointing past the end.
	bad := tiffWithOrientation(binary.BigEndian, 6)
	binary.BigEndian.PutUint32(bad[4:], 0xFFFFFFF0)
	if got := tiffOrientation(bad); got != 1 {
		t.Errorf("bad IFD offset: got %d, want 1", got)
	}
	bad = tiffWithOrientation(binary.BigEndian, 6)
	binary.BigEndian.PutUint16(bad[8:], 0xFFFF)
	bad = bad[:len(bad)-4-12] // cut the entry itself
	if got := tiffOrientation(bad); got != 1 {
		t.Errorf("entry count past end: got %d, want 1", got)
	}
}
