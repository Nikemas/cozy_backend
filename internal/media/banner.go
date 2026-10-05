package media

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"

	"github.com/google/uuid"
	"golang.org/x/image/draw"
)

// Banner pictures (feat/home-banner, internal/banner) go through the same
// pipeline as product photos — decodeUpload (real format by decoding,
// pixel cap, EXIF orientation), the 10 MB file limit, metadata-free
// re-encoding and server-generated keys — but are NOT squared onto white:
// the side picture keeps its aspect ratio and transparency (a cut-out shoe
// on the banner's colored background), the background is a wide photo
// covering the whole banner.

// MaxUploadFileBytes is the largest image file accepted — the same limit
// as POST /admin/api/media/upload, exported for the admin banner form.
const MaxUploadFileBytes = maxUploadFileBytes

// BannerImageKind selects how a banner image is normalized.
type BannerImageKind int

const (
	// BannerPicture is the picture on the right side of the banner.
	BannerPicture BannerImageKind = iota
	// BannerBackground is the optional full-banner background image.
	BannerBackground
)

// bannerProfile is the geometry of one BannerImageKind: the box the
// result is scaled down into (never up) and the smallest accepted source.
type bannerProfile struct {
	maxW, maxH int
	minSide    int
	keepAlpha  bool
}

var bannerProfiles = map[BannerImageKind]bannerProfile{
	BannerPicture:    {maxW: 1200, maxH: 1200, minSide: 200, keepAlpha: true},
	BannerBackground: {maxW: 2400, maxH: 1200, minSide: 400, keepAlpha: false},
}

// bannerKeyPrefix is the object key prefix of banner images:
// banners/<uuid>.jpg|.png — one object per upload, never overwritten.
const bannerKeyPrefix = "banners/"

// encodedImage is one normalized banner image.
type encodedImage struct {
	Data        []byte
	ContentType string
	Ext         string
}

// StoreBannerImage normalizes data as a banner image of the given kind
// and uploads it under a fresh key, which it returns.
func (c *Client) StoreBannerImage(ctx context.Context, data []byte, kind BannerImageKind) (string, error) {
	return storeBannerImage(ctx, c, data, kind)
}

func storeBannerImage(ctx context.Context, store objectStore, data []byte, kind BannerImageKind) (string, error) {
	if len(data) > maxUploadFileBytes {
		return "", errFileTooLarge()
	}
	img, err := normalizeBannerImage(data, kind)
	if err != nil {
		return "", err
	}
	key := bannerKeyPrefix + uuid.NewString() + img.Ext
	if err := store.PutObject(ctx, key, img.Data, img.ContentType); err != nil {
		return "", fmt.Errorf("put %s: %w", key, err)
	}
	return key, nil
}

// normalizeBannerImage decodes data (decodeUpload) and scales it down to
// fit its kind's box. A side picture with transparent pixels stays PNG
// (alpha kept); everything else is flattened onto white and encoded as
// JPEG. Pure function, like normalizeImage.
func normalizeBannerImage(data []byte, kind BannerImageKind) (*encodedImage, error) {
	profile, ok := bannerProfiles[kind]
	if !ok {
		return nil, fmt.Errorf("media: unknown banner image kind %d", kind)
	}
	src, err := decodeUpload(data, profile.minSide)
	if err != nil {
		return nil, err
	}
	scaled := fitWithin(src, profile.maxW, profile.maxH)

	if profile.keepAlpha && !isOpaque(scaled) {
		var buf bytes.Buffer
		if err := png.Encode(&buf, scaled); err != nil {
			return nil, fmt.Errorf("encode png: %w", err)
		}
		return &encodedImage{Data: buf.Bytes(), ContentType: "image/png", Ext: ".png"}, nil
	}

	flat := image.NewRGBA(scaled.Bounds())
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), scaled, scaled.Bounds().Min, draw.Over)
	out, err := encodeJPEG(flat)
	if err != nil {
		return nil, err
	}
	return &encodedImage{Data: out, ContentType: "image/jpeg", Ext: ".jpg"}, nil
}

// fitWithin returns src scaled down (aspect ratio kept) to fit a maxW×maxH
// box, as a fresh RGBA image (alpha preserved). A source already inside
// the box is copied as is — never upscaled.
func fitWithin(src image.Image, maxW, maxH int) *image.RGBA {
	sb := src.Bounds()
	scale := math.Min(1, math.Min(float64(maxW)/float64(sb.Dx()), float64(maxH)/float64(sb.Dy())))
	w := max(1, int(math.Round(float64(sb.Dx())*scale)))
	h := max(1, int(math.Round(float64(sb.Dy())*scale)))
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, sb, draw.Src, nil)
	return dst
}

// isOpaque reports whether every pixel of img is fully opaque.
func isOpaque(img *image.RGBA) bool {
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0xff {
			return false
		}
	}
	return true
}
