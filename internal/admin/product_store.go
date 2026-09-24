// product_store.go saves the whole product form (product row, variants,
// per-point stock, photos) inside ONE transaction — replacing the old
// saveVariants/saveImages sequence of independent catalog repo calls,
// which (a) partially applied on a mid-way error (e.g. variant_in_use left
// the product updated but half its variants changed) and (b) wrote every
// variant's quantity as an absolute value into the first point of sale,
// so a stale form overwrote concurrent sales and, with 2+ points, the
// form's cross-point sum was written back into the first point on every
// save (stock grew with each save).
//
// Stock is now written per (variant × point) cell, and only for cells the
// staff member actually changed, each guarded by the value the form was
// rendered with (optimistic concurrency): UPDATE ... WHERE quantity =
// <original>. A cell whose stock moved in the meantime (a sale, another
// staff member) is reported as a conflict and the whole save rolls back.
//
// The SQL lives here rather than in internal/catalog because catalog's
// repos are bound to *sql.DB (no transaction variant), and the admin form
// is the only caller that needs this all-or-nothing multi-table write.
package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
)

const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

func pgErrCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// variantRowInput is one row of the form's Вариации table. Key identifies
// the row within the submission (the variant id for an existing variant,
// a client-generated "n<N>" for a row added in the browser) so stock cells
// can reference rows that don't have a database id yet.
type variantRowInput struct {
	Key   string
	ID    string // existing variant id, "" for a new row
	Size  string
	Color string
}

// stockCellChange is one edited (variant row × point) stock cell. Orig is
// the quantity the form was rendered with — nil when no stock row existed
// for that pair (rendered as 0).
type stockCellChange struct {
	RowKey  string
	PointID string
	Qty     int
	Orig    *int
}

// productSaveInput is everything saveProduct persists in one transaction.
type productSaveInput struct {
	ProductID string // "" = create
	Product   catalog.ProductInput
	Variants  []variantRowInput
	Stock     []stockCellChange
	Images    []catalog.ImageInput
}

// stockConflict is one cell whose stored quantity no longer matches the
// value the form was rendered with.
type stockConflict struct {
	RowKey  string
	PointID string
	Current int // the quantity currently stored (0 if the row is gone)
	Exists  bool
}

// stockConflictError aborts the save (the transaction rolls back) and
// carries every conflicting cell, so the form can be re-rendered with the
// current values in exactly those cells.
type stockConflictError struct {
	Cells []stockConflict
}

func (e *stockConflictError) Error() string {
	return fmt.Sprintf("stock changed concurrently in %d cell(s)", len(e.Cells))
}

// stockConflictMessage is the locale key of the form-level banner for a
// stockConflictError.
const stockConflictMessage = "admin.stock.err_conflict"

// productStore runs the transactional product-form save.
type productStore struct {
	db *sql.DB
}

func newProductStore(db *sql.DB) *productStore { return &productStore{db: db} }

// validateProductInput mirrors catalog.ProductInput's own (unexported)
// validate() — same codes and messages — since this store writes products
// without going through catalog.ProductRepo.
func validateProductInput(in catalog.ProductInput) error {
	if strings.TrimSpace(in.CategoryID) == "" {
		return apperr.BadRequest("invalid_category_id", "выберите категорию")
	}
	if strings.TrimSpace(in.NameRu) == "" {
		return apperr.BadRequest("invalid_name_ru", "укажите название на русском")
	}
	if strings.TrimSpace(in.NameKy) == "" {
		return apperr.BadRequest("invalid_name_ky", "укажите название на кыргызском")
	}
	if in.BasePrice < 0 {
		return apperr.BadRequest("invalid_base_price", "цена не может быть отрицательной")
	}
	return nil
}

// Save persists in and returns the product's id. Any error rolls back the
// whole save; a *stockConflictError is returned (after rollback) when one
// or more edited stock cells no longer hold their original value.
func (s *productStore) Save(ctx context.Context, in productSaveInput) (string, error) {
	if err := validateProductInput(in.Product); err != nil {
		return "", err
	}
	for _, v := range in.Variants {
		if strings.TrimSpace(v.Size) == "" || strings.TrimSpace(v.Color) == "" {
			return "", apperr.BadRequest("invalid_variant", "у каждой вариации должны быть размер и цвет")
		}
	}
	for _, img := range in.Images {
		if strings.TrimSpace(img.ObjectKey) == "" {
			return "", apperr.BadRequest("invalid_object_key", "object_key обязателен")
		}
	}

	var productID string
	err := dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		id, err := upsertProductTx(ctx, tx, in.ProductID, in.Product)
		if err != nil {
			return err
		}
		productID = id

		idByKey, err := syncVariantsTx(ctx, tx, productID, in.Variants)
		if err != nil {
			return err
		}

		if err := applyStockChangesTx(ctx, tx, idByKey, in.Stock); err != nil {
			return err
		}

		return replaceImagesTx(ctx, tx, productID, in.Images)
	})
	if err != nil {
		return "", err
	}
	return productID, nil
}

// upsertProductTx creates (productID == "") or updates the product row.
// Update leaves is_active untouched — the form never edits it (that's the
// list's Деактивировать/Активировать action).
func upsertProductTx(ctx context.Context, tx *sql.Tx, productID string, in catalog.ProductInput) (string, error) {
	if productID == "" {
		const q = `
			INSERT INTO products (category_id, name_ru, name_ky, description_ru, description_ky, brand, base_price, is_active)
			VALUES ($1, $2, $3, $4, $5, $6, $7, true)
			RETURNING id`
		var id string
		err := tx.QueryRowContext(ctx, q, in.CategoryID, in.NameRu, in.NameKy, in.DescriptionRu, in.DescriptionKy,
			in.Brand, in.BasePrice).Scan(&id)
		if err != nil {
			return "", translateProductErr(err)
		}
		return id, nil
	}

	const q = `
		UPDATE products
		SET category_id = $2, name_ru = $3, name_ky = $4, description_ru = $5, description_ky = $6,
		    brand = $7, base_price = $8, updated_at = now()
		WHERE id = $1
		RETURNING id`
	var id string
	err := tx.QueryRowContext(ctx, q, productID, in.CategoryID, in.NameRu, in.NameKy, in.DescriptionRu, in.DescriptionKy,
		in.Brand, in.BasePrice).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", apperr.NotFound("product_not_found", "товар не найден")
	}
	if err != nil {
		return "", translateProductErr(err)
	}
	return id, nil
}

func translateProductErr(err error) error {
	if pgErrCode(err) == pgForeignKeyViolation {
		return apperr.BadRequest("invalid_category_id", "категория не найдена")
	}
	return err
}

// syncVariantsTx diffs rows against productID's current variants: rows
// whose ID is one of them are updated (size/color only — sku and
// price_override, set by the xlsx import, are left intact), other rows are
// created, and variants missing from rows are deleted. Deletes run first so
// removing "42/Белый" and re-adding the same combo in one save doesn't trip
// the (product_id, size, color) unique constraint. Returns row key ->
// variant id for every surviving row.
func syncVariantsTx(ctx context.Context, tx *sql.Tx, productID string, rows []variantRowInput) (map[string]string, error) {
	existingIDs, err := variantIDsTx(ctx, tx, productID)
	if err != nil {
		return nil, err
	}
	existing := make(map[string]bool, len(existingIDs))
	for _, id := range existingIDs {
		existing[id] = true
	}

	kept := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.ID != "" && existing[row.ID] {
			kept[row.ID] = true
		}
	}

	for _, id := range existingIDs {
		if kept[id] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM product_variants WHERE id = $1`, id); err != nil {
			if pgErrCode(err) == pgForeignKeyViolation {
				return nil, apperr.Conflict("variant_in_use", "нельзя удалить вариацию: по ней есть заказы — поставьте ей остаток 0")
			}
			return nil, err
		}
	}

	idByKey := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.ID != "" && existing[row.ID] {
			const q = `UPDATE product_variants SET size = $2, color = $3 WHERE id = $1`
			if _, err := tx.ExecContext(ctx, q, row.ID, row.Size, row.Color); err != nil {
				return nil, translateVariantErr(err)
			}
			idByKey[row.Key] = row.ID
			continue
		}

		const q = `
			INSERT INTO product_variants (product_id, size, color)
			VALUES ($1, $2, $3)
			RETURNING id`
		var id string
		if err := tx.QueryRowContext(ctx, q, productID, row.Size, row.Color).Scan(&id); err != nil {
			return nil, translateVariantErr(err)
		}
		idByKey[row.Key] = id
	}
	return idByKey, nil
}

func variantIDsTx(ctx context.Context, tx *sql.Tx, productID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM product_variants WHERE product_id = $1 ORDER BY id`, productID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func translateVariantErr(err error) error {
	if pgErrCode(err) == pgUniqueViolation {
		return apperr.Conflict("variant_exists", "вариации повторяются: такой размер и цвет уже есть в таблице")
	}
	return err
}

// applyStockChangesTx writes each changed cell with an optimistic check
// against its original value. Every cell is attempted (so the error lists
// all conflicts, not just the first); any conflict returns a
// *stockConflictError, which rolls the whole transaction back.
func applyStockChangesTx(ctx context.Context, tx *sql.Tx, idByKey map[string]string, changes []stockCellChange) error {
	var conflicts []stockConflict
	for _, c := range changes {
		variantID, ok := idByKey[c.RowKey]
		if !ok {
			continue // the row was removed in this same submission
		}
		applied, err := writeStockCellTx(ctx, tx, variantID, c.PointID, c.Qty, c.Orig)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		current, exists, err := readStockTx(ctx, tx, variantID, c.PointID)
		if err != nil {
			return err
		}
		conflicts = append(conflicts, stockConflict{RowKey: c.RowKey, PointID: c.PointID, Current: current, Exists: exists})
	}
	if len(conflicts) > 0 {
		return &stockConflictError{Cells: conflicts}
	}
	return nil
}

// writeStockCellTx sets variant×point stock to qty only if it still holds
// orig (or, for orig == nil, only if no stock row exists yet). applied is
// false when that precondition failed — i.e. a concurrent change.
func writeStockCellTx(ctx context.Context, tx *sql.Tx, variantID, pointID string, qty int, orig *int) (applied bool, err error) {
	if qty < 0 {
		return false, apperr.BadRequest("invalid_quantity", "количество не может быть отрицательным")
	}

	var res sql.Result
	if orig == nil {
		const q = `
			INSERT INTO stock (variant_id, point_id, quantity, updated_at)
			VALUES ($1, $2, $3, now())
			ON CONFLICT (variant_id, point_id) DO NOTHING`
		res, err = tx.ExecContext(ctx, q, variantID, pointID, qty)
	} else {
		const q = `
			UPDATE stock SET quantity = $3, updated_at = now()
			WHERE variant_id = $1 AND point_id = $2 AND quantity = $4`
		res, err = tx.ExecContext(ctx, q, variantID, pointID, qty, *orig)
	}
	if err != nil {
		if pgErrCode(err) == pgForeignKeyViolation {
			return false, apperr.BadRequest("invalid_variant_or_point", "вариация или точка продаж не найдена — обновите страницу")
		}
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func readStockTx(ctx context.Context, tx *sql.Tx, variantID, pointID string) (qty int, exists bool, err error) {
	err = tx.QueryRowContext(ctx, `SELECT quantity FROM stock WHERE variant_id = $1 AND point_id = $2`, variantID, pointID).Scan(&qty)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return qty, true, nil
}

// replaceImagesTx is catalog.ImageRepo.ReplaceForProduct's delete+insert,
// run inside the form's transaction.
func replaceImagesTx(ctx context.Context, tx *sql.Tx, productID string, images []catalog.ImageInput) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM product_images WHERE product_id = $1`, productID); err != nil {
		return err
	}
	const q = `INSERT INTO product_images (product_id, object_key, sort_order, color) VALUES ($1, $2, $3, $4)`
	for _, img := range images {
		if _, err := tx.ExecContext(ctx, q, productID, img.ObjectKey, img.SortOrder, img.Color); err != nil {
			return err
		}
	}
	return nil
}
