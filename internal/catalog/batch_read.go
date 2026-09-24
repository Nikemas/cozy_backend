package catalog

import "context"

// ListByProductIDs is the batch form of ListByProduct: every variant of
// every product in productIDs in one query, keyed by product_id, each
// list in the same (size, color) order ListByProduct uses. Products with
// no variants are absent from the map.
func (r *VariantRepo) ListByProductIDs(ctx context.Context, productIDs []string) (map[string][]Variant, error) {
	out := make(map[string][]Variant, len(productIDs))
	if len(productIDs) == 0 {
		return out, nil
	}

	const q = `
		SELECT id, product_id, size, color, sku, price_override
		FROM product_variants
		WHERE product_id = ANY($1)
		ORDER BY product_id, size, color`

	rows, err := r.db.QueryContext(ctx, q, productIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var v Variant
		if err := rows.Scan(&v.ID, &v.ProductID, &v.Size, &v.Color, &v.SKU, &v.PriceOverride); err != nil {
			return nil, err
		}
		out[v.ProductID] = append(out[v.ProductID], v)
	}
	return out, rows.Err()
}

// ListByProductIDs is the batch form of ImageRepo.ListByProduct: every
// image of every product in productIDs in one query, keyed by product_id,
// each list ordered like ListByProduct (general photos first, then by
// color, each by sort_order).
func (r *ImageRepo) ListByProductIDs(ctx context.Context, productIDs []string) (map[string][]ProductImage, error) {
	out := make(map[string][]ProductImage, len(productIDs))
	if len(productIDs) == 0 {
		return out, nil
	}

	const q = `
		SELECT id, product_id, object_key, sort_order, color
		FROM product_images
		WHERE product_id = ANY($1)
		ORDER BY product_id, color NULLS FIRST, sort_order`

	rows, err := r.db.QueryContext(ctx, q, productIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var pi ProductImage
		if err := rows.Scan(&pi.ID, &pi.ProductID, &pi.ObjectKey, &pi.SortOrder, &pi.Color); err != nil {
			return nil, err
		}
		out[pi.ProductID] = append(out[pi.ProductID], pi)
	}
	return out, rows.Err()
}
