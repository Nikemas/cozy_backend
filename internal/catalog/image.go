package catalog

import (
	"context"
	"database/sql"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// ProductImage mirrors a row of the `product_images` table. object_key
// points at a file in MinIO — this package never talks to MinIO itself, it
// only stores the key a separate upload flow hands back.
type ProductImage struct {
	ID        string `json:"id"`
	ProductID string `json:"product_id"`
	ObjectKey string `json:"object_key"`
	SortOrder int    `json:"sort_order"`
}

type ImageRepo struct {
	db *sql.DB
}

func NewImageRepo(db *sql.DB) *ImageRepo {
	return &ImageRepo{db: db}
}

// PrimaryForProducts returns each product's first image (lowest
// sort_order), keyed by product_id — products with no image are simply
// absent from the map. Used by the storefront (internal/web) to render
// grid thumbnails without an N+1 query per product.
func (r *ImageRepo) PrimaryForProducts(ctx context.Context, productIDs []string) (map[string]ProductImage, error) {
	if len(productIDs) == 0 {
		return map[string]ProductImage{}, nil
	}

	const q = `
		SELECT DISTINCT ON (product_id) id, product_id, object_key, sort_order
		FROM product_images
		WHERE product_id = ANY($1)
		ORDER BY product_id, sort_order`

	rows, err := r.db.QueryContext(ctx, q, productIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]ProductImage, len(productIDs))
	for rows.Next() {
		var pi ProductImage
		if err := rows.Scan(&pi.ID, &pi.ProductID, &pi.ObjectKey, &pi.SortOrder); err != nil {
			return nil, err
		}
		out[pi.ProductID] = pi
	}
	return out, rows.Err()
}

// ImageInput carries the writable fields of one product image.
type ImageInput struct {
	ObjectKey string
	SortOrder int
}

// ReplaceForProduct replaces the full set of images for productID with
// images, inside one transaction — the admin UI always submits the whole
// ordered list at once (add/remove/reorder collapse to the same PUT), and a
// delete-then-insert done outside a transaction would let a concurrent GET
// briefly see zero images. Returns apperr.NotFound if productID doesn't
// reference an existing product.
func (r *ImageRepo) ReplaceForProduct(ctx context.Context, productID string, images []ImageInput) ([]ProductImage, error) {
	for _, img := range images {
		if strings.TrimSpace(img.ObjectKey) == "" {
			return nil, apperr.BadRequest("invalid_object_key", "object_key обязателен")
		}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM product_images WHERE product_id = $1`, productID); err != nil {
		return nil, err
	}

	result := make([]ProductImage, 0, len(images))
	for _, img := range images {
		const q = `
			INSERT INTO product_images (product_id, object_key, sort_order)
			VALUES ($1, $2, $3)
			RETURNING id, product_id, object_key, sort_order`

		var pi ProductImage
		err := tx.QueryRowContext(ctx, q, productID, img.ObjectKey, img.SortOrder).
			Scan(&pi.ID, &pi.ProductID, &pi.ObjectKey, &pi.SortOrder)
		if pgErrCode(err) == pgForeignKeyViolation {
			return nil, apperr.NotFound("product_not_found", "товар не найден")
		}
		if err != nil {
			return nil, err
		}
		result = append(result, pi)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
