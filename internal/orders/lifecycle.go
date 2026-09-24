package orders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// This file owns every order status transition and its side effects, so
// the rules live in one place whatever the entry point (admin HTML, admin
// JSON API, customer API/site, payment callback, expiry job):
//
//   - every transition writes an order_status_history row in the same
//     transaction;
//   - an online_card order can only be confirmed once it is paid;
//   - only the owner may cancel from the admin side;
//   - every cancellation returns the reserved stock in the same
//     transaction, cancels a still-pending payment, and flags a paid one
//     as refund_required.

// ActorType says who caused a status change (order_status_history.actor_type).
type ActorType string

const (
	ActorStaff    ActorType = "staff"
	ActorCustomer ActorType = "customer"
	ActorSystem   ActorType = "system"
)

// Actor is who performs a transition.
type Actor struct {
	Type    ActorType
	StaffID string     // ActorStaff only
	Role    staff.Role // ActorStaff only
}

// SystemActor is the payment callback / expiry job.
var SystemActor = Actor{Type: ActorSystem}

// StatusChange is one order_status_history row.
type StatusChange struct {
	FromStatus   *OrderStatus `json:"from_status"`
	ToStatus     OrderStatus  `json:"to_status"`
	ActorType    ActorType    `json:"actor_type"`
	ActorStaffID *string      `json:"actor_staff_id,omitempty"`
	ActorName    *string      `json:"actor_name,omitempty"`
	Note         *string      `json:"note,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
}

// ErrNotCancellable is returned when a customer tries to cancel an order
// that is past 'placed' or already paid online.
var ErrNotCancellable = apperr.Conflict("order_not_cancellable",
	"заказ уже нельзя отменить — свяжитесь с магазином")

// orderColumns is the column list every scanOrderRow caller selects.
const orderColumns = `id, order_number, customer_id, address_id, point_id, status, payment_method, payment_status,
	total_amount, delivery_fee, refund_required, comment, created_at, updated_at`

// staffActor derives the acting staff member from ctx (put there by
// staff.RequireRole / the admin auth gate).
func staffActor(ctx context.Context) (Actor, bool) {
	st, ok := staff.FromContext(ctx)
	if !ok || st == nil {
		return Actor{}, false
	}
	return Actor{Type: ActorStaff, StaffID: st.ID, Role: st.Role}, true
}

// lockOrderTx loads one order FOR UPDATE by the given predicate.
func lockOrderTx(ctx context.Context, tx *sql.Tx, predicate string, args ...any) (*Order, error) {
	q := `SELECT ` + orderColumns + ` FROM orders WHERE ` + predicate + ` FOR UPDATE`
	var o Order
	if err := scanOrderRow(tx.QueryRowContext(ctx, q, args...), &o); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("order_not_found", "заказ не найден")
		}
		return nil, err
	}
	return &o, nil
}

// recordStatusTx appends one order_status_history row.
func recordStatusTx(ctx context.Context, tx *sql.Tx, orderID string, from *OrderStatus, to OrderStatus, actor Actor, note string) error {
	var staffID, notePtr *string
	if actor.Type == ActorStaff && actor.StaffID != "" {
		id := actor.StaffID
		staffID = &id
	}
	if note != "" {
		notePtr = &note
	}
	const q = `
		INSERT INTO order_status_history (order_id, from_status, to_status, actor_type, actor_staff_id, note)
		VALUES ($1, $2, $3, $4, $5, $6)`
	_, err := tx.ExecContext(ctx, q, orderID, from, to, actor.Type, staffID, notePtr)
	return err
}

// restockOrderTx returns every item of orderID to pointID's stock. Rows
// are upserted in variant-id order — the same lock order CreateOrder uses
// — so a cancel and a checkout can't deadlock. Upsert rather than UPDATE:
// the stock row could have been deleted by an admin since the order was
// placed; the returned goods still exist.
func restockOrderTx(ctx context.Context, tx *sql.Tx, orderID, pointID string) error {
	const itemsQ = `SELECT variant_id, quantity FROM order_items WHERE order_id = $1`
	rows, err := tx.QueryContext(ctx, itemsQ, orderID)
	if err != nil {
		return err
	}
	qtyByVariant := map[string]int{}
	for rows.Next() {
		var variantID string
		var qty int
		if err := rows.Scan(&variantID, &qty); err != nil {
			_ = rows.Close()
			return err
		}
		qtyByVariant[variantID] += qty
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	variantIDs := make([]string, 0, len(qtyByVariant))
	for id := range qtyByVariant {
		variantIDs = append(variantIDs, id)
	}
	sort.Strings(variantIDs)

	const restockQ = `
		INSERT INTO stock (variant_id, point_id, quantity, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (variant_id, point_id)
		DO UPDATE SET quantity = stock.quantity + EXCLUDED.quantity, updated_at = now()`
	for _, variantID := range variantIDs {
		if _, err := tx.ExecContext(ctx, restockQ, variantID, pointID, qtyByVariant[variantID]); err != nil {
			return err
		}
	}
	return nil
}

// cancelLockedTx cancels o (already locked FOR UPDATE by the caller and
// not yet cancelled/delivered): stock back, pending payment cancelled,
// paid payment flagged for refund, history row written. o is updated in
// place.
func cancelLockedTx(ctx context.Context, tx *sql.Tx, o *Order, actor Actor, note string) error {
	if o.PointID == nil || *o.PointID == "" {
		// CreateOrder always records the fulfillment point, so this means
		// the row was edited by hand; refuse rather than lose stock silently.
		return fmt.Errorf("orders: order %s has no point_id, cannot return stock", o.ID)
	}
	if err := restockOrderTx(ctx, tx, o.ID, *o.PointID); err != nil {
		return err
	}

	newPayment := o.PaymentStatus
	refund := o.RefundRequired
	if o.PaymentMethod == PaymentOnlineCard && o.PaymentStatus != nil {
		switch *o.PaymentStatus {
		case PaymentPending:
			// The customer may still be on the bank page. Cancel the
			// payment so a late "paid" callback is recognised as money for
			// a cancelled order (refund_required) instead of being applied.
			if _, err := tx.ExecContext(ctx,
				`UPDATE payments SET status = 'cancelled', updated_at = now() WHERE order_id = $1 AND status = 'pending'`,
				o.ID); err != nil {
				return err
			}
			c := PaymentCancelled
			newPayment = &c
		case PaymentPaid:
			refund = true
		}
	}

	const q = `
		UPDATE orders SET status = 'cancelled', payment_status = $2, refund_required = $3, updated_at = now()
		WHERE id = $1
		RETURNING updated_at`
	if err := tx.QueryRowContext(ctx, q, o.ID, newPayment, refund).Scan(&o.UpdatedAt); err != nil {
		return err
	}
	from := o.Status
	if err := recordStatusTx(ctx, tx, o.ID, &from, StatusCancelled, actor, note); err != nil {
		return err
	}
	o.Status = StatusCancelled
	o.PaymentStatus = newPayment
	o.RefundRequired = refund
	return nil
}

// transitionLockedTx moves o (locked FOR UPDATE) to `to`, enforcing the
// state machine and the payment gate.
func transitionLockedTx(ctx context.Context, tx *sql.Tx, o *Order, to OrderStatus, actor Actor, note string) error {
	if !validStatusTransition(o.Status, to) {
		return apperr.BadRequest("invalid_status_transition",
			fmt.Sprintf("нельзя перевести заказ из статуса %q в %q", o.Status, to))
	}
	if to == StatusConfirmed && !paymentAllowsConfirm(o) {
		return apperr.Conflict("payment_not_completed",
			"заказ с онлайн-оплатой можно подтвердить только после оплаты")
	}
	if to == StatusCancelled {
		return cancelLockedTx(ctx, tx, o, actor, note)
	}

	const q = `UPDATE orders SET status = $1, updated_at = now() WHERE id = $2 RETURNING updated_at`
	if err := tx.QueryRowContext(ctx, q, to, o.ID).Scan(&o.UpdatedAt); err != nil {
		return err
	}
	from := o.Status
	if err := recordStatusTx(ctx, tx, o.ID, &from, to, actor, note); err != nil {
		return err
	}
	o.Status = to
	return nil
}

// paymentAllowsConfirm: cash-on-delivery orders are confirmed freely; an
// online_card order only once its payment is paid.
func paymentAllowsConfirm(o *Order) bool {
	if o.PaymentMethod != PaymentOnlineCard {
		return true
	}
	return o.PaymentStatus != nil && *o.PaymentStatus == PaymentPaid
}

// customerCanCancel: only a 'placed' order that hasn't been paid online.
func customerCanCancel(o *Order) bool {
	if o.Status != StatusPlaced {
		return false
	}
	if o.PaymentMethod == PaymentOnlineCard && o.PaymentStatus != nil && *o.PaymentStatus == PaymentPaid {
		return false
	}
	return true
}

// CustomerCanCancel reports whether the customer-facing "Отменить" action
// applies to o (used by the site to decide whether to show the button).
func CustomerCanCancel(o Order) bool { return customerCanCancel(&o) }

// AdminUpdateStatus transitions order idOrNumber to newStatus on behalf of
// the staff member in ctx. Rules enforced here, not in the handlers:
// the state machine (400 invalid_status_transition), owner-only cancel
// (403 cancel_forbidden), online orders confirmable only when paid (409
// payment_not_completed). Cancelling returns stock. Point-based RBAC
// (point_staff restricted to their own point) stays with the caller, which
// has already loaded the order to check it.
func (s *Service) AdminUpdateStatus(ctx context.Context, idOrNumber string, newStatus OrderStatus) (*Order, error) {
	actor, ok := staffActor(ctx)
	if !ok {
		return nil, apperr.Forbidden("forbidden", "действие доступно только сотрудникам")
	}
	if newStatus == StatusCancelled && actor.Role != staff.RoleOwner {
		return nil, apperr.Forbidden("cancel_forbidden", "только владелец может отменить заказ")
	}

	var o *Order
	var from OrderStatus
	err := dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		o, err = lockOrderTx(ctx, tx, orderKeyPredicate(idOrNumber, 1), idOrNumber)
		if err != nil {
			return err
		}
		from = o.Status
		return transitionLockedTx(ctx, tx, o, newStatus, actor, "")
	})
	if err != nil {
		return nil, err
	}

	list := []Order{*o}
	// The status change has committed; loading items is best-effort
	// enrichment and must not hide that from the caller's notification.
	itemsErr := s.attachItems(ctx, list)
	s.notifyStatusChanged(list[0], from)
	if itemsErr != nil {
		return nil, itemsErr
	}
	return &list[0], nil
}

// CancelByCustomer cancels customerID's own order idOrNumber: allowed only
// while it is 'placed' and not paid online (409 order_not_cancellable
// otherwise). Stock goes back and a pending online payment is cancelled in
// the same transaction.
func (s *Service) CancelByCustomer(ctx context.Context, customerID, idOrNumber string) (*Order, error) {
	var o *Order
	err := dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		o, err = lockOrderTx(ctx, tx, "customer_id = $1 AND "+orderKeyPredicate(idOrNumber, 2), customerID, idOrNumber)
		if err != nil {
			return err
		}
		if !customerCanCancel(o) {
			return ErrNotCancellable
		}
		return cancelLockedTx(ctx, tx, o, Actor{Type: ActorCustomer}, "")
	})
	if err != nil {
		return nil, err
	}

	list := []Order{*o}
	itemsErr := s.attachItems(ctx, list)
	s.notifyCancelledByCustomer(list[0])
	if itemsErr != nil {
		return nil, itemsErr
	}
	return &list[0], nil
}

// CancelUnpaidOrderTx cancels orderID and returns its reserved stock,
// inside the caller's transaction — the compensating action for a failed,
// cancelled or expired online payment (the caller has already written the
// payment's final status). It only acts on an order still 'placed':
// otherwise it returns (false, nil) and touches nothing, which also makes a
// repeated failure callback unable to restock twice.
func CancelUnpaidOrderTx(ctx context.Context, tx *sql.Tx, orderID, note string) (bool, error) {
	o, err := lockOrderTx(ctx, tx, "id = $1", orderID)
	if err != nil {
		return false, err
	}
	if o.Status != StatusPlaced {
		return false, nil
	}
	if err := cancelLockedTx(ctx, tx, o, SystemActor, note); err != nil {
		return false, err
	}
	return true, nil
}

// MarkRefundRequiredTx flags orderID as needing a refund (money arrived
// for an order that is cancelled or whose payment had already failed).
func MarkRefundRequiredTx(ctx context.Context, tx *sql.Tx, orderID string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE orders SET refund_required = true, updated_at = now() WHERE id = $1`, orderID)
	return err
}

// StatusHistory returns orderID's status changes, oldest first, with the
// acting staff member's name where known.
func (s *Service) StatusHistory(ctx context.Context, orderID string) ([]StatusChange, error) {
	const q = `
		SELECT h.from_status, h.to_status, h.actor_type, h.actor_staff_id, st.name, h.note, h.created_at
		FROM order_status_history h
		LEFT JOIN staff st ON st.id = h.actor_staff_id
		WHERE h.order_id = $1
		ORDER BY h.created_at, h.id`
	rows, err := s.db.QueryContext(ctx, q, orderID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []StatusChange{}
	for rows.Next() {
		var c StatusChange
		if err := rows.Scan(&c.FromStatus, &c.ToStatus, &c.ActorType, &c.ActorStaffID, &c.ActorName, &c.Note, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NotifyOrderPaid tells staff about a paid online_card order — the
// "new order" message is held back until the money is in (cash orders get
// it at creation). Called by internal/payments after the paid callback has
// committed. Best-effort: a lookup failure is logged by the caller.
func (s *Service) NotifyOrderPaid(ctx context.Context, orderID string) error {
	o, err := s.getOrderAnyCustomer(ctx, orderID)
	if err != nil {
		return err
	}
	if o.Status == StatusCancelled {
		return nil
	}
	s.notifyCreated(*o)
	return nil
}
