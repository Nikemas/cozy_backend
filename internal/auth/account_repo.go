package auth

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
)

// accountStore deletes customer accounts — an interface for tests.
type accountStore interface {
	deleteCustomer(ctx context.Context, customerID string) error
}

type accountRepo struct {
	db *sql.DB
}

// terminalOrderStatuses are the order_status values after which an order
// needs nothing more from the customer (000010_create_orders).
const terminalOrderStatuses = `('delivered', 'cancelled')`

// deleteCustomer implements the account-deletion contract in one
// transaction (row-locked, so it can't race a concurrent checkout):
//
//   - refuses with 409 has_active_orders while any order is not yet
//     delivered/cancelled — the shop still has to call/deliver/refund;
//   - deletes cart, favorites, device tokens and every saved address not
//     referenced by an order;
//   - addresses referenced by orders are kept (they are part of the order
//     record for accounting) but lose their label/default flag;
//   - revokes all refresh tokens;
//   - anonymizes the customer row (phone -> 'deleted:<id>', name -> NULL,
//     deleted_at set). The row itself stays because orders reference it,
//     and the freed phone number can register again as a new customer.
//
// Deleting an already-deleted account is a no-op.
func (r *accountRepo) deleteCustomer(ctx context.Context, customerID string) error {
	return dbtx.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		var deletedAt sql.NullTime
		err := tx.QueryRowContext(ctx, `SELECT deleted_at FROM customers WHERE id = $1 FOR UPDATE`, customerID).Scan(&deletedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFound("customer_not_found", "покупатель не найден")
		}
		if err != nil {
			return err
		}
		if deletedAt.Valid {
			return nil
		}

		var active bool
		const activeQ = `SELECT EXISTS(SELECT 1 FROM orders WHERE customer_id = $1 AND status NOT IN ` + terminalOrderStatuses + `)`
		if err := tx.QueryRowContext(ctx, activeQ, customerID).Scan(&active); err != nil {
			return err
		}
		if active {
			return apperr.Conflict("has_active_orders", "есть незавершённые заказы — удалить аккаунт можно после их доставки или отмены")
		}

		for _, q := range []string{
			`DELETE FROM cart_items WHERE customer_id = $1`,
			`DELETE FROM favorites WHERE customer_id = $1`,
			`DELETE FROM device_tokens WHERE customer_id = $1`,
			`DELETE FROM customer_addresses a WHERE a.customer_id = $1
				AND NOT EXISTS (SELECT 1 FROM orders o WHERE o.address_id = a.id)`,
			`UPDATE customer_addresses SET label = NULL, is_default = false WHERE customer_id = $1`,
			`UPDATE refresh_tokens SET revoked_at = now() WHERE customer_id = $1 AND revoked_at IS NULL`,
			`UPDATE customers SET phone = 'deleted:' || id::text, name = NULL, deleted_at = now(), updated_at = now()
				WHERE id = $1`,
		} {
			if _, err := tx.ExecContext(ctx, q, customerID); err != nil {
				return err
			}
		}
		return nil
	})
}
