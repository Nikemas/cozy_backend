package media

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"strings"

	"golang.org/x/image/draw"
)

// Product photos uploaded before 23.11.2026 were saved with a white
// legacyPaddingRatio frame around the shoe. The admin uploads only the
// normalized variants (the original file is not kept), so the frame is
// removed from the stored `full` variant itself: the shoe was fitted
// inside the inner square, which means cropping to it never cuts the
// shoe — it only drops the white frame. The result is scaled back to
// fullSize/thumbSize exactly as a fresh upload would be.

const (
	// legacyFrameWhiteMin is the lowest channel value still counted as
	// white in the frame: JPEG at quality 85 leaves the flat white frame
	// at 250–255, while a real background is rarely that bright on all
	// three channels at once.
	legacyFrameWhiteMin = 245
	// legacyFrameSampleStep samples every Nth pixel of the frame — the
	// check only has to tell "framed" from "not framed", not every pixel.
	legacyFrameSampleStep = 3
	// legacyFrameInset keeps the check this many pixels clear of the
	// inner edge, where JPEG ringing from the shoe bleeds into the frame.
	legacyFrameInset = 8
)

// stripLegacyFrame returns full without the legacy white frame, at
// fullSize×fullSize, and ok == true. If full is not a fullSize square or
// its frame is not white (a photo uploaded after the change, or already
// reprocessed), it returns ok == false and the photo must be left as is.
func stripLegacyFrame(full image.Image) (*image.RGBA, bool) {
	b := full.Bounds()
	if b.Dx() != fullSize || b.Dy() != fullSize {
		return nil, false
	}
	frame := int(math.Round(float64(fullSize) * legacyPaddingRatio))
	if !frameIsWhite(full, frame-legacyFrameInset) {
		return nil, false
	}
	inner := image.Rect(b.Min.X+frame, b.Min.Y+frame, b.Max.X-frame, b.Max.Y-frame)
	out := image.NewRGBA(image.Rect(0, 0, fullSize, fullSize))
	draw.CatmullRom.Scale(out, out.Bounds(), full, inner, draw.Src, nil)
	return out, true
}

// frameIsWhite reports whether the band of width w along all four edges
// of img is white (every channel ≥ legacyFrameWhiteMin) at every sampled
// pixel.
func frameIsWhite(img image.Image, w int) bool {
	if w <= 0 {
		return false
	}
	b := img.Bounds()
	isWhite := func(x, y int) bool {
		r, g, bl, _ := img.At(x, y).RGBA()
		const minV = legacyFrameWhiteMin << 8
		return r >= minV && g >= minV && bl >= minV
	}
	for y := b.Min.Y; y < b.Max.Y; y += legacyFrameSampleStep {
		for x := b.Min.X; x < b.Max.X; x += legacyFrameSampleStep {
			inBand := x < b.Min.X+w || x >= b.Max.X-w || y < b.Min.Y+w || y >= b.Max.Y-w
			if inBand && !isWhite(x, y) {
				return false
			}
		}
	}
	return true
}

// reframeLegacyPhoto turns the bytes of a legacy `full` JPEG into new
// full and thumb JPEGs without the white frame. ok == false means the
// photo has no legacy frame and must be left alone.
func reframeLegacyPhoto(fullJPEG []byte) (*normalizedImage, bool, error) {
	// The header first: only a fullSize square can carry the legacy frame,
	// so anything else is never decoded.
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(fullJPEG))
	if err != nil {
		return nil, false, fmt.Errorf("decode jpeg: %w", err)
	}
	if cfg.Width != fullSize || cfg.Height != fullSize {
		return nil, false, nil
	}
	src, err := jpeg.Decode(bytes.NewReader(fullJPEG))
	if err != nil {
		return nil, false, fmt.Errorf("decode jpeg: %w", err)
	}
	full, ok := stripLegacyFrame(src)
	if !ok {
		return nil, false, nil
	}
	thumb := image.NewRGBA(image.Rect(0, 0, thumbSize, thumbSize))
	draw.CatmullRom.Scale(thumb, thumb.Bounds(), full, full.Bounds(), draw.Src, nil)

	fullOut, err := encodeJPEG(full)
	if err != nil {
		return nil, false, err
	}
	thumbOut, err := encodeJPEG(thumb)
	if err != nil {
		return nil, false, err
	}
	return &normalizedImage{Full: fullOut, Thumb: thumbOut}, true, nil
}

// photoReprocessStore is what ReprocessLegacyPhotos needs from MinIO.
type photoReprocessStore interface {
	GetObject(ctx context.Context, objectKey string) ([]byte, error)
	PutObject(ctx context.Context, objectKey string, data []byte, contentType string) error
}

// ReprocessStats counts what ReprocessLegacyPhotos did (or, in a dry
// run, would do).
type ReprocessStats struct {
	Checked  int // product_images rows with a full/thumb photo
	Reframed int // photos whose white frame was (or would be) removed
	Skipped  int // photos without the legacy frame, left as is
	Failed   int // photos that could not be read or written; left as is
}

// ReprocessLegacyPhotos removes the legacy white frame from the product
// photos marked product_images.legacy_frame (migration 000053 marks every
// photo that existed before the change; photos uploaded after it are
// never marked). Each marked photo is checked: if its frame really is
// white it is reframed under fresh object keys (objects are immutable and
// cached for a year, so they are never overwritten) and its row is pointed
// at them; either way the mark is cleared, so a second run finds nothing
// to do and a new photo with a white studio background is never touched.
// The old objects are left in the bucket, so a row can be pointed back if
// needed. In a dry run nothing is written and every photo that would be
// reframed is listed. A failure on one photo is logged to logw and
// counted, its mark stays for the next run, and the run goes on.
func ReprocessLegacyPhotos(ctx context.Context, db *sql.DB, store photoReprocessStore, dryRun bool, logw io.Writer) (ReprocessStats, error) {
	var stats ReprocessStats
	rows, err := db.QueryContext(ctx,
		`SELECT id, object_key FROM product_images
		 WHERE legacy_frame AND object_key LIKE 'products/%' || $1 ORDER BY id`, fullSuffix)
	if err != nil {
		return stats, fmt.Errorf("list product photos: %w", err)
	}
	type photo struct{ id, key string }
	var photos []photo
	for rows.Next() {
		var p photo
		if err := rows.Scan(&p.id, &p.key); err != nil {
			_ = rows.Close()
			return stats, fmt.Errorf("scan product photo: %w", err)
		}
		photos = append(photos, p)
	}
	if err := rows.Close(); err != nil {
		return stats, fmt.Errorf("list product photos: %w", err)
	}
	if err := rows.Err(); err != nil {
		return stats, fmt.Errorf("list product photos: %w", err)
	}

	for _, p := range photos {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		stats.Checked++
		reframed, err := reprocessOne(ctx, db, store, p.id, p.key, dryRun)
		switch {
		case err != nil:
			stats.Failed++
			_, _ = fmt.Fprintf(logw, "FAIL  %s %s: %v\n", p.id, p.key, err)
		case reframed && dryRun:
			stats.Reframed++
			_, _ = fmt.Fprintf(logw, "WOULD %s %s\n", p.id, p.key)
		case reframed:
			stats.Reframed++
			_, _ = fmt.Fprintf(logw, "OK    %s %s\n", p.id, p.key)
		default:
			stats.Skipped++
		}
	}
	return stats, nil
}

func reprocessOne(ctx context.Context, db *sql.DB, store photoReprocessStore, id, key string, dryRun bool) (bool, error) {
	data, err := store.GetObject(ctx, key)
	if err != nil {
		return false, fmt.Errorf("read: %w", err)
	}
	img, ok, err := reframeLegacyPhoto(data)
	if err != nil {
		return false, err
	}
	if dryRun {
		return ok, nil
	}
	if !ok {
		// No white frame to strip: nothing to do, and nothing to recheck.
		return false, clearLegacyFrame(ctx, db, id, key, key)
	}
	fullKey, thumbKey := newVariantKeys()
	if err := store.PutObject(ctx, thumbKey, img.Thumb, "image/jpeg"); err != nil {
		return false, fmt.Errorf("write thumb: %w", err)
	}
	if err := store.PutObject(ctx, fullKey, img.Full, "image/jpeg"); err != nil {
		return false, fmt.Errorf("write full: %w", err)
	}
	if err := clearLegacyFrame(ctx, db, id, key, fullKey); err != nil {
		return false, fmt.Errorf("%w (new objects %s unused)", err, strings.TrimSuffix(fullKey, fullSuffix))
	}
	return true, nil
}

// clearLegacyFrame points the row at newKey and clears its mark. The
// WHERE on the old key guards against an admin replacing the photo while
// the run is in progress (the replacement is a new, unmarked photo).
func clearLegacyFrame(ctx context.Context, db *sql.DB, id, oldKey, newKey string) error {
	res, err := db.ExecContext(ctx,
		`UPDATE product_images SET object_key = $1, legacy_frame = false
		 WHERE id = $2 AND object_key = $3 AND legacy_frame`, newKey, id, oldKey)
	if err != nil {
		return fmt.Errorf("update row: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("row changed during the run, left as is")
	}
	return nil
}
