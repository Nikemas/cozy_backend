package web

import (
	"context"

	"github.com/Nikemas/cozy_backend/internal/media"
)

// variantPhotos returns a thumbnail URL per variant id — the photo tagged
// with the variant's colour if there is one, else a general product photo,
// else any photo of the product. Variants whose product has no photos are
// absent (templates fall back to the shoe icon). One query for a whole
// cart / order list.
func (h *handlers) variantPhotos(ctx context.Context, variantIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(variantIDs))
	if len(variantIDs) == 0 {
		return out, nil
	}
	const q = `
		SELECT DISTINCT ON (pv.id) pv.id, pi.object_key
		FROM product_variants pv
		JOIN product_images pi ON pi.product_id = pv.product_id
		WHERE pv.id = ANY($1)
		ORDER BY pv.id, (pi.color IS DISTINCT FROM pv.color), (pi.color IS NOT NULL), pi.sort_order`
	rows, err := h.db.QueryContext(ctx, q, variantIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		out[id] = h.photoURL(media.ThumbKey(key))
	}
	return out, rows.Err()
}

// attachCartPhotos fills CartLineView.PhotoURL for every line.
func (h *handlers) attachCartPhotos(ctx context.Context, lines []CartLineView) error {
	ids := make([]string, len(lines))
	for i, l := range lines {
		ids[i] = l.VariantID
	}
	photos, err := h.variantPhotos(ctx, ids)
	if err != nil {
		return err
	}
	for i := range lines {
		lines[i].PhotoURL = photos[lines[i].VariantID]
	}
	return nil
}
