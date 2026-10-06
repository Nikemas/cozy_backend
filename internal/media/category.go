package media

import "context"

// Category photos (feat/category-photo, catalog.Category.ImageKey) are the
// pictures on the mobile app's catalog category tiles. They reuse the
// banner side-picture pipeline as is — decodeUpload (real format by
// decoding, pixel cap, EXIF orientation), the 10 MB file limit,
// metadata-free re-encoding, server-generated keys, transparency kept for
// a cut-out shoe — only in a smaller box, since a tile is never shown
// larger than a phone's half width.

// categoryImageMaxSide is the box (px) a category photo is scaled down
// into; categoryImageMinSide the smallest accepted source side.
const (
	categoryImageMaxSide = 800
	categoryImageMinSide = 200
)

var categoryProfile = bannerProfile{
	maxW: categoryImageMaxSide, maxH: categoryImageMaxSide,
	minSide: categoryImageMinSide, keepAlpha: true,
}

// categoryKeyPrefix is the object key prefix of category photos:
// categories/<uuid>.jpg|.png — one object per upload, never overwritten.
const categoryKeyPrefix = "categories/"

// StoreCategoryImage normalizes data as a category photo and uploads it
// under a fresh key, which it returns.
func (c *Client) StoreCategoryImage(ctx context.Context, data []byte) (string, error) {
	return storeCategoryImage(ctx, c, data)
}

func storeCategoryImage(ctx context.Context, store objectStore, data []byte) (string, error) {
	return storeFittedImage(ctx, store, data, categoryProfile, categoryKeyPrefix)
}

// normalizeCategoryImage is the pure normalization step of
// storeCategoryImage.
func normalizeCategoryImage(data []byte) (*encodedImage, error) {
	return normalizeFitted(data, categoryProfile)
}
