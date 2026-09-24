package orders

import (
	"context"
	"log/slog"
	"sync/atomic"
)

// ItemPhotoURLs turns a product photo's object key into its full-size and
// thumbnail URLs (config.PublicObjectURL + media.ThumbKey) — orders can't
// import config/media without a cycle-prone dependency, so cmd/server
// installs it with SetItemPhotoURLs.
type ItemPhotoURLs func(objectKey string) (photoURL, thumbURL string)

var itemPhotoURLs atomic.Value // ItemPhotoURLs

// SetItemPhotoURLs installs the process-wide photo URL builder. Until it
// is called, order items carry no product_id/photo_url/thumb_url (and no
// extra query is made).
func SetItemPhotoURLs(fn ItemPhotoURLs) {
	itemPhotoURLs.Store(fn)
}

// itemPhotoQuery resolves, per variant, its product and the photo that
// best shows it: one tagged with the variant's own color, then a general
// (untagged) photo, then any — each by sort_order. Same ranking as the
// admin orders list (internal/admin/orders_list_meta.go thumbRankSQL).
const itemPhotoQuery = `
	SELECT pv.id, pv.product_id, img.object_key
	FROM product_variants pv
	LEFT JOIN LATERAL (
		SELECT pi.object_key FROM product_images pi
		WHERE pi.product_id = pv.product_id
		ORDER BY CASE WHEN pi.color = pv.color THEN 0 WHEN pi.color IS NULL THEN 1 ELSE 2 END, pi.sort_order
		LIMIT 1
	) img ON true
	WHERE pv.id = ANY($1)`

// attachItemPhotos fills ProductID/PhotoURL/ThumbURL on every item of
// every order in list with one batched query. Best-effort: photos are
// decoration, so a failure is logged and the orders are returned without
// them rather than failing the request.
func (s *Service) attachItemPhotos(ctx context.Context, list []Order) {
	build, _ := itemPhotoURLs.Load().(ItemPhotoURLs)
	if build == nil || len(list) == 0 {
		return
	}
	seen := map[string]bool{}
	var variantIDs []string
	for _, o := range list {
		for _, it := range o.Items {
			if !seen[it.VariantID] {
				seen[it.VariantID] = true
				variantIDs = append(variantIDs, it.VariantID)
			}
		}
	}
	if len(variantIDs) == 0 {
		return
	}

	rows, err := s.db.QueryContext(ctx, itemPhotoQuery, variantIDs)
	if err != nil {
		slog.WarnContext(ctx, "orders: loading item photos failed", "err", err)
		return
	}
	defer func() { _ = rows.Close() }()

	type variantPhoto struct {
		productID string
		objectKey *string
	}
	byVariant := make(map[string]variantPhoto, len(variantIDs))
	for rows.Next() {
		var id string
		var vp variantPhoto
		if err := rows.Scan(&id, &vp.productID, &vp.objectKey); err != nil {
			slog.WarnContext(ctx, "orders: reading item photos failed", "err", err)
			return
		}
		byVariant[id] = vp
	}
	if err := rows.Err(); err != nil {
		slog.WarnContext(ctx, "orders: reading item photos failed", "err", err)
		return
	}

	for i := range list {
		for j := range list[i].Items {
			it := &list[i].Items[j]
			vp, ok := byVariant[it.VariantID]
			if !ok {
				continue
			}
			productID := vp.productID
			it.ProductID = &productID
			if vp.objectKey != nil && *vp.objectKey != "" {
				photo, thumb := build(*vp.objectKey)
				it.PhotoURL, it.ThumbURL = &photo, &thumb
			}
		}
	}
}
