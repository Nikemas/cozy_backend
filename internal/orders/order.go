package orders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
)

// OrderStatus mirrors the order_status enum (migration
// 000010_create_orders).
type OrderStatus string

const (
	StatusPlaced          OrderStatus = "placed"
	StatusConfirmed       OrderStatus = "confirmed"
	StatusCourierAssigned OrderStatus = "courier_assigned"
	StatusDelivered       OrderStatus = "delivered"
	StatusCancelled       OrderStatus = "cancelled"
)

// PaymentMethod mirrors the payment_method enum (000010, with 'online'
// renamed to 'online_card' by 000021). The site checkout still only
// creates cash_on_delivery orders; online_card orders come from
// POST /api/v1/orders via CreateOnlineOrder (see payment.go and
// internal/payments).
type PaymentMethod string

const (
	PaymentOnlineCard     PaymentMethod = "online_card"
	PaymentCashOnDelivery PaymentMethod = "cash_on_delivery"
)

// Order mirrors one row of orders (+ its order_items).
type Order struct {
	ID            string        `json:"id"`
	OrderNumber   string        `json:"order_number"` // "COZY-YYYYMMDD-NNN"
	CustomerID    string        `json:"customer_id"`
	AddressID     *string       `json:"address_id,omitempty"`
	PointID       *string       `json:"point_id,omitempty"`
	Status        OrderStatus   `json:"status"`
	PaymentMethod PaymentMethod `json:"payment_method"`
	// PaymentStatus is the current state of an online_card order's payment
	// (orders.payment_status, migration 000021); nil for cash_on_delivery.
	PaymentStatus *PaymentStatus `json:"payment_status,omitempty"`
	TotalAmount   float64        `json:"total_amount"`
	Comment       *string        `json:"comment,omitempty"`
	Items         []OrderItem    `json:"items"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// OrderItem mirrors one row of order_items — a price/size/color snapshot
// taken at checkout time, independent of later catalog edits.
type OrderItem struct {
	ID                  string  `json:"id"`
	OrderID             string  `json:"order_id"`
	VariantID           string  `json:"variant_id"`
	ProductNameSnapshot string  `json:"product_name_snapshot"`
	SizeSnapshot        string  `json:"size_snapshot"`
	ColorSnapshot       string  `json:"color_snapshot"`
	Quantity            int     `json:"quantity"`
	Price               float64 `json:"price"`
}

// OrderItemInput is what CreateOrder needs per line. Only VariantID/Qty
// come from the client — price/name/size/color are always recomputed
// server-side from the current catalog+stock row, never trusted from the
// request.
type OrderItemInput struct {
	VariantID string
	Quantity  int
}

// Service owns order lifecycle: creating one (from a cart, or directly
// for the product page's "Заказать сразу" instant-buy button — both call
// this same method, per web-plan Architecture Decisions), and listing/
// fetching a customer's own orders.
type Service struct {
	db       *sql.DB
	notifier Notifier // nil → process default, see notifier.go
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// variantSnapshot is what CreateOrder needs to know about a variant at
// order time: the current product name/size/color/price, recomputed from
// the catalog tables inside the same transaction that locks stock — never
// trusted from the client. It intentionally doesn't reuse
// internal/catalog's types: that package is owned by Task 2 and exposes
// no locking-aware, batch-by-ID accessor, so CreateOrder reads the
// underlying product_variants/products tables directly instead of adding
// one there.
type variantSnapshot struct {
	ProductName string
	Size        string
	Color       string
	Price       float64
}

// CreateOrder creates an order from items, atomically decrementing stock
// (a transaction with SELECT ... FOR UPDATE, locked in a stable
// variant-id order to avoid cross-order deadlocks) and snapshotting
// price/size/color into order_items. Exactly one of addressID/
// pickupPointID must be set — delivery vs. self-pickup. For a delivery
// order, the fulfilling point_of_sale is chosen automatically (the first
// active point whose stock covers every line) since the client only
// supplies an address, not a warehouse.
func (s *Service) CreateOrder(ctx context.Context, customerID string, items []OrderItemInput, addressID, pickupPointID *string) (*Order, error) {
	return s.createOrder(ctx, customerID, items, addressID, pickupPointID, PaymentCashOnDelivery, nil)
}

// createOrder is CreateOrder's body, parameterized by payment method.
// afterInsert (may be nil) runs inside the same transaction once the order
// and its items are inserted — CreateOnlineOrder uses it to insert the
// pending payments row atomically with the order.
func (s *Service) createOrder(ctx context.Context, customerID string, items []OrderItemInput, addressID, pickupPointID *string,
	method PaymentMethod, afterInsert func(tx *sql.Tx, order *Order) error) (*Order, error) {
	if err := validateFulfillment(addressID, pickupPointID); err != nil {
		return nil, err
	}
	qtyByVariant, variantIDs, err := mergeItemQuantities(items)
	if err != nil {
		return nil, err
	}

	var order Order
	err = dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		if addressID != nil {
			if err := verifyAddressOwnership(ctx, tx, customerID, *addressID); err != nil {
				return err
			}
		}

		fulfillmentPointID := ""
		if pickupPointID != nil {
			active, err := pointIsActive(ctx, tx, *pickupPointID)
			if err != nil {
				return err
			}
			if !active {
				return apperr.NotFound("pickup_point_not_found", "точка самовывоза не найдена")
			}
			fulfillmentPointID = *pickupPointID
		}

		snapshots, err := loadVariantSnapshots(ctx, tx, variantIDs)
		if err != nil {
			return err
		}
		for _, id := range variantIDs {
			if _, ok := snapshots[id]; !ok {
				return apperr.NotFound("variant_not_found", "товар недоступен")
			}
		}

		if fulfillmentPointID == "" {
			fulfillmentPointID, err = pickFulfillmentPoint(ctx, tx, variantIDs, qtyByVariant)
			if err != nil {
				return err
			}
		}

		if err := lockAndDecrementStock(ctx, tx, fulfillmentPointID, variantIDs, qtyByVariant); err != nil {
			return err
		}

		orderNumber, err := nextOrderNumber(ctx, tx, time.Now())
		if err != nil {
			return err
		}

		order = Order{
			OrderNumber:   orderNumber,
			CustomerID:    customerID,
			AddressID:     addressID,
			Status:        StatusPlaced,
			PaymentMethod: method,
		}
		if method == PaymentOnlineCard {
			pending := PaymentPending
			order.PaymentStatus = &pending
		}
		if pickupPointID != nil {
			order.PointID = pickupPointID
		} else {
			// Delivery order: point_id isn't the customer's choice here, it's
			// which warehouse fulfilled it — recorded alongside address_id, not
			// instead of it (see migration 000019's comment on why the two
			// columns aren't a strict either/or at the DB level).
			fp := fulfillmentPointID
			order.PointID = &fp
		}

		orderItems := make([]OrderItem, 0, len(variantIDs))
		var total float64
		for _, variantID := range variantIDs {
			snap := snapshots[variantID]
			qty := qtyByVariant[variantID]
			total += snap.Price * float64(qty)
			orderItems = append(orderItems, OrderItem{
				VariantID:           variantID,
				ProductNameSnapshot: snap.ProductName,
				SizeSnapshot:        snap.Size,
				ColorSnapshot:       snap.Color,
				Quantity:            qty,
				Price:               snap.Price,
			})
		}
		order.TotalAmount = math.Round(total*100) / 100

		const insertOrderQ = `
			INSERT INTO orders (order_number, customer_id, address_id, point_id, status, payment_method, payment_status, total_amount)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING id, created_at, updated_at`
		err = tx.QueryRowContext(ctx, insertOrderQ,
			order.OrderNumber, order.CustomerID, order.AddressID, order.PointID, order.Status, order.PaymentMethod, order.PaymentStatus, order.TotalAmount,
		).Scan(&order.ID, &order.CreatedAt, &order.UpdatedAt)
		if err != nil {
			return err
		}

		const insertItemQ = `
			INSERT INTO order_items (order_id, variant_id, product_name_snapshot, size_snapshot, color_snapshot, quantity, price)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id`
		for i := range orderItems {
			orderItems[i].OrderID = order.ID
			err := tx.QueryRowContext(ctx, insertItemQ,
				order.ID, orderItems[i].VariantID, orderItems[i].ProductNameSnapshot,
				orderItems[i].SizeSnapshot, orderItems[i].ColorSnapshot, orderItems[i].Quantity, orderItems[i].Price,
			).Scan(&orderItems[i].ID)
			if err != nil {
				return err
			}
		}
		order.Items = orderItems
		if afterInsert != nil {
			return afterInsert(tx, &order)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifyCreated(order)
	return &order, nil
}

// ListOrders returns customerID's own orders, newest first, each with its
// items attached.
func (s *Service) ListOrders(ctx context.Context, customerID string) ([]Order, error) {
	const q = `
		SELECT id, order_number, customer_id, address_id, point_id, status, payment_method, payment_status, total_amount, comment, created_at, updated_at
		FROM orders
		WHERE customer_id = $1
		ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, q, customerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	list := []Order{}
	for rows.Next() {
		var o Order
		if err := scanOrder(rows, &o); err != nil {
			return nil, err
		}
		list = append(list, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := s.attachItems(ctx, list); err != nil {
		return nil, err
	}
	return list, nil
}

// orderKeyPredicate matches an order by UUID primary key or by
// order_number, whichever idOrNumber looks like. The old combined
// `id::text = $n OR order_number = $n` could use neither index and scanned
// the whole orders table on every lookup. The result is concatenated into
// SQL but is one of two fixed strings — idOrNumber itself stays a bound
// parameter.
func orderKeyPredicate(idOrNumber string, n int) string {
	if uuid.Validate(idOrNumber) == nil {
		return fmt.Sprintf("id = $%d::uuid", n)
	}
	return fmt.Sprintf("order_number = $%d", n)
}

// GetOrder returns one order, scoped to customerID so a customer can't
// fetch someone else's order by guessing an ID. orderID may be either the
// order's UUID primary key or its human-readable order_number (the
// `/order/{orderNumber}/done` route only has the latter to work with).
func (s *Service) GetOrder(ctx context.Context, customerID, orderID string) (*Order, error) {
	q := `
		SELECT id, order_number, customer_id, address_id, point_id, status, payment_method, payment_status, total_amount, comment, created_at, updated_at
		FROM orders
		WHERE customer_id = $1 AND ` + orderKeyPredicate(orderID, 2)

	var o Order
	row := s.db.QueryRowContext(ctx, q, customerID, orderID)
	if err := scanOrderRow(row, &o); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("order_not_found", "заказ не найден")
		}
		return nil, err
	}

	list := []Order{o}
	if err := s.attachItems(ctx, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

// rowScanner is satisfied by both *sql.Rows and *sql.Row, so scanOrder can
// back both ListOrders (many rows) and GetOrder (one row) with the same
// column list.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanOrder(rows *sql.Rows, o *Order) error {
	return scanOrderRow(rows, o)
}

func scanOrderRow(row rowScanner, o *Order) error {
	return row.Scan(&o.ID, &o.OrderNumber, &o.CustomerID, &o.AddressID, &o.PointID,
		&o.Status, &o.PaymentMethod, &o.PaymentStatus, &o.TotalAmount, &o.Comment, &o.CreatedAt, &o.UpdatedAt)
}

// attachItems batch-loads order_items for every order in list and appends
// them onto the matching Order in place.
func (s *Service) attachItems(ctx context.Context, list []Order) error {
	if len(list) == 0 {
		return nil
	}

	ids := make([]string, len(list))
	idxByID := make(map[string]int, len(list))
	for i, o := range list {
		ids[i] = o.ID
		idxByID[o.ID] = i
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	q := fmt.Sprintf(`
		SELECT id, order_id, variant_id, product_name_snapshot, size_snapshot, color_snapshot, quantity, price
		FROM order_items
		WHERE order_id IN (%s)
		ORDER BY id`, strings.Join(placeholders, ", "))

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var it OrderItem
		if err := rows.Scan(&it.ID, &it.OrderID, &it.VariantID, &it.ProductNameSnapshot,
			&it.SizeSnapshot, &it.ColorSnapshot, &it.Quantity, &it.Price); err != nil {
			return err
		}
		i, ok := idxByID[it.OrderID]
		if !ok {
			continue
		}
		list[i].Items = append(list[i].Items, it)
	}
	return rows.Err()
}

// validateFulfillment enforces "exactly one of addressID/pickupPointID" —
// delivery XOR self-pickup. A pure function so the rule is unit-testable
// without a database.
func validateFulfillment(addressID, pickupPointID *string) error {
	hasAddr := addressID != nil && *addressID != ""
	hasPoint := pickupPointID != nil && *pickupPointID != ""
	if hasAddr == hasPoint {
		return apperr.BadRequest("invalid_fulfillment", "укажите ровно один способ получения: адрес доставки или точку самовывоза")
	}
	return nil
}

// mergeItemQuantities validates items and folds duplicate variant lines
// into one quantity per variant (so ordering the same variant twice in
// one request doesn't double-lock/double-count it). Returns the merged
// map plus a stable, sorted slice of the variant IDs involved — sorted so
// every caller locks stock rows in the same order, which is what avoids a
// classic lock-ordering deadlock between two concurrent orders that share
// a variant.
func mergeItemQuantities(items []OrderItemInput) (map[string]int, []string, error) {
	if len(items) == 0 {
		return nil, nil, apperr.BadRequest("empty_order", "нет товаров для заказа")
	}

	merged := make(map[string]int, len(items))
	for _, it := range items {
		if it.VariantID == "" {
			return nil, nil, apperr.BadRequest("invalid_item", "не указан вариант товара")
		}
		if it.Quantity <= 0 {
			return nil, nil, apperr.BadRequest("invalid_qty", "количество должно быть больше нуля")
		}
		merged[it.VariantID] += it.Quantity
	}

	variantIDs := make([]string, 0, len(merged))
	for id := range merged {
		variantIDs = append(variantIDs, id)
	}
	sort.Strings(variantIDs)

	return merged, variantIDs, nil
}

func verifyAddressOwnership(ctx context.Context, tx *sql.Tx, customerID, addressID string) error {
	var exists bool
	const q = `SELECT EXISTS(SELECT 1 FROM customer_addresses WHERE id = $1 AND customer_id = $2)`
	if err := tx.QueryRowContext(ctx, q, addressID, customerID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return apperr.NotFound("address_not_found", "адрес не найден")
	}
	return nil
}

func pointIsActive(ctx context.Context, tx *sql.Tx, pointID string) (bool, error) {
	var active bool
	const q = `SELECT is_active FROM points_of_sale WHERE id = $1`
	err := tx.QueryRowContext(ctx, q, pointID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return active, nil
}

// loadVariantSnapshots reads the current product_variants+products row
// for each variant ID — the authoritative size/color/price at order time.
// A missing key in the result for a requested ID means that variant
// doesn't exist or its product is inactive.
func loadVariantSnapshots(ctx context.Context, tx *sql.Tx, variantIDs []string) (map[string]variantSnapshot, error) {
	if len(variantIDs) == 0 {
		return map[string]variantSnapshot{}, nil
	}

	placeholders := make([]string, len(variantIDs))
	args := make([]any, len(variantIDs))
	for i, id := range variantIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	q := fmt.Sprintf(`
		SELECT pv.id, pv.size, pv.color, pv.price_override, p.name_ru, p.base_price
		FROM product_variants pv
		JOIN products p ON p.id = pv.product_id
		WHERE pv.id IN (%s) AND p.is_active = true`, strings.Join(placeholders, ", "))

	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]variantSnapshot, len(variantIDs))
	for rows.Next() {
		var id, size, color, name string
		var override *float64
		var base float64
		if err := rows.Scan(&id, &size, &color, &override, &name, &base); err != nil {
			return nil, err
		}
		price := base
		if override != nil {
			price = *override
		}
		out[id] = variantSnapshot{ProductName: name, Size: size, Color: color, Price: price}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// pickFulfillmentPoint chooses which points_of_sale a delivery order gets
// packed from: the first active point (by id, for determinism) whose
// stock covers every requested line. Returns apperr.Conflict if no single
// point can fulfill the whole order — splitting one order across multiple
// points is out of scope for this MVP.
func pickFulfillmentPoint(ctx context.Context, tx *sql.Tx, variantIDs []string, qtyByVariant map[string]int) (string, error) {
	const q = `SELECT id FROM points_of_sale WHERE is_active = true ORDER BY id`
	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return "", err
	}
	var pointIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return "", err
		}
		pointIDs = append(pointIDs, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", err
	}
	_ = rows.Close()

	for _, pointID := range pointIDs {
		ok, err := pointCoversStock(ctx, tx, pointID, variantIDs, qtyByVariant)
		if err != nil {
			return "", err
		}
		if ok {
			return pointID, nil
		}
	}
	return "", apperr.Conflict("insufficient_stock", "нет точки, где есть весь заказ в наличии")
}

func pointCoversStock(ctx context.Context, tx *sql.Tx, pointID string, variantIDs []string, qtyByVariant map[string]int) (bool, error) {
	const q = `SELECT quantity FROM stock WHERE variant_id = $1 AND point_id = $2`
	for _, variantID := range variantIDs {
		var qty int
		err := tx.QueryRowContext(ctx, q, variantID, pointID).Scan(&qty)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if qty < qtyByVariant[variantID] {
			return false, nil
		}
	}
	return true, nil
}

// lockAndDecrementStock locks (SELECT ... FOR UPDATE) and decrements the
// stock row for each variant at pointID, in the caller's already-sorted
// variantIDs order — the stable lock order that keeps two concurrent
// orders touching an overlapping set of variants from deadlocking each
// other. Re-checks quantity under the lock (not just under
// pickFulfillmentPoint's earlier, unlocked read) since stock can change
// between that check and this one.
func lockAndDecrementStock(ctx context.Context, tx *sql.Tx, pointID string, variantIDs []string, qtyByVariant map[string]int) error {
	const lockQ = `SELECT quantity FROM stock WHERE variant_id = $1 AND point_id = $2 FOR UPDATE`
	const updQ = `UPDATE stock SET quantity = quantity - $1, updated_at = now() WHERE variant_id = $2 AND point_id = $3`

	for _, variantID := range variantIDs {
		var qty int
		err := tx.QueryRowContext(ctx, lockQ, variantID, pointID).Scan(&qty)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.Conflict("insufficient_stock", "товар закончился в наличии")
		}
		if err != nil {
			return err
		}

		need := qtyByVariant[variantID]
		if qty < need {
			return apperr.Conflict("insufficient_stock", "недостаточно товара на складе")
		}

		if _, err := tx.ExecContext(ctx, updQ, need, variantID, pointID); err != nil {
			return err
		}
	}
	return nil
}

// nextOrderNumber generates "COZY-YYYYMMDD-NNN" (migration
// 000010_create_orders' comment for the order_number column), NNN being a
// per-day, zero-padded sequence. pg_advisory_xact_lock serializes
// concurrent order creation for the same day within the transaction (and
// releases automatically at commit/rollback) so two simultaneous orders
// can't both compute the same NNN and collide on order_number's UNIQUE
// constraint.
func nextOrderNumber(ctx context.Context, tx *sql.Tx, now time.Time) (string, error) {
	dateStr := now.UTC().Format("20060102")

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "order_number:"+dateStr); err != nil {
		return "", err
	}

	var count int
	const q = `SELECT COUNT(*) FROM orders WHERE order_number LIKE $1`
	if err := tx.QueryRowContext(ctx, q, "COZY-"+dateStr+"-%").Scan(&count); err != nil {
		return "", err
	}

	return formatOrderNumber(dateStr, count+1), nil
}

// formatOrderNumber is split out from nextOrderNumber so the "COZY-
// YYYYMMDD-NNN" formatting/padding can be unit-tested without a database.
func formatOrderNumber(dateStr string, seq int) string {
	return fmt.Sprintf("COZY-%s-%03d", dateStr, seq)
}
