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
	"unicode/utf8"

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
	// TotalAmount is what the customer pays: items + DeliveryFee.
	TotalAmount float64 `json:"total_amount"`
	// DeliveryFee is the delivery charge included in TotalAmount (0 for
	// self-pickup and for orders placed before delivery was charged).
	DeliveryFee float64 `json:"delivery_fee"`
	// DeliveryZone is the zone the delivery fee was charged for; null for
	// pickup and for delivery orders placed while no zone was active.
	DeliveryZone *OrderDeliveryZone `json:"delivery_zone"`
	// RefundRequired: money was taken for an order that ended up cancelled
	// — staff must refund it through the bank.
	RefundRequired bool        `json:"refund_required"`
	Comment        *string     `json:"comment,omitempty"`
	Items          []OrderItem `json:"items"`
	// History is the status-change log; only admin reads fill it.
	History   []StatusChange `json:"status_history,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
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
	notifier Notifier  // nil → process default, see notifier.go
	settings *Settings // nil → process default, see settings.go
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
	Active      bool // products.is_active
}

// label names the line in customer-facing errors: "Air Max, 42".
func (v variantSnapshot) label() string {
	if v.Size == "" {
		return v.ProductName
	}
	return v.ProductName + ", " + v.Size
}

// PlaceOrderInput is everything needed to place an order. Only VariantID/
// Quantity per line come from the client — prices, names, sizes and the
// delivery fee are always computed server-side.
type PlaceOrderInput struct {
	CustomerID    string
	Items         []OrderItemInput
	AddressID     *string // delivery address, XOR PickupPointID
	PickupPointID *string
	// DeliveryZoneID is the delivery zone (delivery orders only; ignored
	// for pickup). Required while at least one zone is active; the fee is
	// then the zone's, else Settings.DeliveryFee.
	DeliveryZoneID *string
	// Comment is the customer's note to the store (≤ MaxCommentLen runes;
	// blank is stored as NULL).
	Comment string
	// IdempotencyKey (optional, ≤ MaxIdempotencyKeyLen): a retry with the
	// same key by the same customer within IdempotencyWindow returns the
	// order created by the first attempt instead of a second one.
	IdempotencyKey string
	// ClearCart removes the ordered variants from the customer's cart in
	// the same transaction (the site checkout places orders from the cart).
	ClearCart bool
}

// CreateOrder creates a cash_on_delivery order from items — the short
// form of PlaceOrder without comment/idempotency.
func (s *Service) CreateOrder(ctx context.Context, customerID string, items []OrderItemInput, addressID, pickupPointID *string) (*Order, error) {
	o, _, err := s.PlaceOrder(ctx, PlaceOrderInput{
		CustomerID: customerID, Items: items, AddressID: addressID, PickupPointID: pickupPointID,
	})
	return o, err
}

// PlaceOrder creates a cash_on_delivery order, atomically decrementing
// stock (a transaction with SELECT ... FOR UPDATE, locked in a stable
// variant-id order to avoid cross-order deadlocks) and snapshotting
// price/size/color into order_items. Exactly one of AddressID/
// PickupPointID must be set — delivery vs. self-pickup. For a delivery
// order, the fulfilling point_of_sale is chosen automatically (the first
// active point whose stock covers every line) and the delivery fee is
// added to the total.
//
// created=false means in.IdempotencyKey matched an earlier order, which is
// returned unchanged.
func (s *Service) PlaceOrder(ctx context.Context, in PlaceOrderInput) (*Order, bool, error) {
	return s.createOrder(ctx, in, PaymentCashOnDelivery, nil)
}

// createOrder is PlaceOrder's body, parameterized by payment method.
// afterInsert (may be nil) runs inside the same transaction once the order
// and its items are inserted — CreateOnlineOrder uses it to insert the
// pending payments row atomically with the order.
func (s *Service) createOrder(ctx context.Context, in PlaceOrderInput, method PaymentMethod,
	afterInsert func(tx *sql.Tx, order *Order) error) (*Order, bool, error) {
	addressID, pickupPointID := in.AddressID, in.PickupPointID
	if err := validateFulfillment(addressID, pickupPointID); err != nil {
		return nil, false, err
	}
	// Normalize "" to nil so the XOR above and the fee below agree.
	if addressID != nil && *addressID == "" {
		addressID = nil
	}
	if pickupPointID != nil && *pickupPointID == "" {
		pickupPointID = nil
	}
	qtyByVariant, variantIDs, err := mergeItemQuantities(in.Items)
	if err != nil {
		return nil, false, err
	}
	comment, err := normalizeComment(in.Comment)
	if err != nil {
		return nil, false, err
	}
	idemKey, err := normalizeIdempotencyKey(in.IdempotencyKey)
	if err != nil {
		return nil, false, err
	}
	settings := s.currentSettings()
	customerID := in.CustomerID

	var order Order
	var replayID string
	var zone *DeliveryZone
	err = dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		// One order at a time per customer: serializes the idempotency
		// lookup and the open-orders count against a concurrent double
		// submit (released at commit/rollback).
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "order_customer:"+customerID); err != nil {
			return err
		}
		if idemKey != nil {
			id, err := findIdempotentOrder(ctx, tx, customerID, *idemKey)
			if err != nil {
				return err
			}
			if id != "" {
				replayID = id
				return nil
			}
		}
		if settings.MaxOpenOrders > 0 {
			if err := checkOpenOrdersLimit(ctx, tx, customerID, settings.MaxOpenOrders); err != nil {
				return err
			}
		}

		if addressID != nil {
			if err := verifyAddressOwnership(ctx, tx, customerID, *addressID); err != nil {
				return err
			}
			z, err := resolveDeliveryZoneTx(ctx, tx, in.DeliveryZoneID)
			if err != nil {
				return err
			}
			zone = z
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
			snap, ok := snapshots[id]
			if !ok {
				return apperr.NotFound("variant_not_found", "товар из заказа не найден — обновите корзину")
			}
			if !snap.Active {
				return apperr.Conflict("product_unavailable",
					fmt.Sprintf("товар «%s» больше не продаётся — уберите его из корзины", snap.label()))
			}
		}

		if fulfillmentPointID == "" {
			fulfillmentPointID, err = pickFulfillmentPoint(ctx, tx, variantIDs, qtyByVariant, snapshots)
			if err != nil {
				return err
			}
		}

		if err := lockAndDecrementStock(ctx, tx, fulfillmentPointID, variantIDs, qtyByVariant, snapshots); err != nil {
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
			Comment:       comment,
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
		var itemsTotal float64
		for _, variantID := range variantIDs {
			snap := snapshots[variantID]
			qty := qtyByVariant[variantID]
			itemsTotal += snap.Price * float64(qty)
			orderItems = append(orderItems, OrderItem{
				VariantID:           variantID,
				ProductNameSnapshot: snap.ProductName,
				SizeSnapshot:        snap.Size,
				ColorSnapshot:       snap.Color,
				Quantity:            qty,
				Price:               snap.Price,
			})
		}
		order.DeliveryFee = deliveryFee(settings, addressID != nil, zone, itemsTotal)
		order.TotalAmount = roundSom(itemsTotal + order.DeliveryFee)
		var zoneID *string
		if zone != nil {
			zoneID = &zone.ID
			order.DeliveryZone = &OrderDeliveryZone{ID: zone.ID, NameRu: zone.NameRu, NameKy: zone.NameKy}
		}

		const insertOrderQ = `
			INSERT INTO orders (order_number, customer_id, address_id, point_id, status, payment_method, payment_status,
				total_amount, delivery_fee, comment, idempotency_key, delivery_zone_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			RETURNING id, created_at, updated_at`
		err = tx.QueryRowContext(ctx, insertOrderQ,
			order.OrderNumber, order.CustomerID, order.AddressID, order.PointID, order.Status, order.PaymentMethod, order.PaymentStatus,
			order.TotalAmount, order.DeliveryFee, order.Comment, idemKey, zoneID,
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

		if err := recordStatusTx(ctx, tx, order.ID, nil, StatusPlaced, Actor{Type: ActorCustomer}, ""); err != nil {
			return err
		}
		if in.ClearCart {
			if err := clearCartLinesTx(ctx, tx, customerID, variantIDs); err != nil {
				return err
			}
		}
		if afterInsert != nil {
			return afterInsert(tx, &order)
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if replayID != "" {
		existing, err := s.GetOrder(ctx, customerID, replayID)
		if err != nil {
			return nil, false, err
		}
		return existing, false, nil
	}
	// Staff hear about a cash order now; about an online order only once
	// it's paid (NotifyOrderPaid, called from the payment callback) — an
	// unpaid online order is not something to start packing.
	if method == PaymentCashOnDelivery {
		s.notifyCreated(order)
	}
	return &order, true, nil
}

// findIdempotentOrder returns the id of customerID's order created with
// key within IdempotencyWindow, or "". An order carrying the same key but
// older than the window has its key cleared so the key can be reused (the
// unique index would otherwise reject the new order).
func findIdempotentOrder(ctx context.Context, tx *sql.Tx, customerID, key string) (string, error) {
	const q = `
		SELECT id, created_at > now() - make_interval(secs => $3)
		FROM orders WHERE customer_id = $1 AND idempotency_key = $2`
	var id string
	var fresh bool
	err := tx.QueryRowContext(ctx, q, customerID, key, IdempotencyWindow.Seconds()).Scan(&id, &fresh)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if fresh {
		return id, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE orders SET idempotency_key = NULL WHERE id = $1`, id); err != nil {
		return "", err
	}
	return "", nil
}

// FindByIdempotencyKey returns customerID's order placed with key within
// IdempotencyWindow, or nil if there is none.
func (s *Service) FindByIdempotencyKey(ctx context.Context, customerID, key string) (*Order, error) {
	k, err := normalizeIdempotencyKey(key)
	if err != nil || k == nil {
		return nil, err
	}
	const q = `
		SELECT id FROM orders
		WHERE customer_id = $1 AND idempotency_key = $2 AND created_at > now() - make_interval(secs => $3)`
	var id string
	err = s.db.QueryRowContext(ctx, q, customerID, *k, IdempotencyWindow.Seconds()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.GetOrder(ctx, customerID, id)
}

// checkOpenOrdersLimit rejects a new order when the customer already has
// max non-final orders.
func checkOpenOrdersLimit(ctx context.Context, tx *sql.Tx, customerID string, max int) error {
	const q = `
		SELECT COUNT(*) FROM orders
		WHERE customer_id = $1 AND status IN ('placed', 'confirmed', 'courier_assigned')`
	var n int
	if err := tx.QueryRowContext(ctx, q, customerID).Scan(&n); err != nil {
		return err
	}
	if n >= max {
		return apperr.Conflict("too_many_open_orders",
			fmt.Sprintf("у вас уже %d незавершённых заказов — дождитесь их выполнения или отмените ненужные", n))
	}
	return nil
}

// clearCartLinesTx deletes the ordered variants from the customer's cart.
func clearCartLinesTx(ctx context.Context, tx *sql.Tx, customerID string, variantIDs []string) error {
	if len(variantIDs) == 0 {
		return nil
	}
	args := make([]any, 0, len(variantIDs)+1)
	args = append(args, customerID)
	placeholders := make([]string, len(variantIDs))
	for i, id := range variantIDs {
		args = append(args, id)
		placeholders[i] = fmt.Sprintf("$%d", i+2)
	}
	q := `DELETE FROM cart_items WHERE customer_id = $1 AND variant_id IN (` + strings.Join(placeholders, ", ") + `)`
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}

// normalizeComment trims the customer's note; blank → nil.
func normalizeComment(c string) (*string, error) {
	c = strings.TrimSpace(c)
	if c == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(c) > MaxCommentLen {
		return nil, apperr.BadRequest("comment_too_long",
			fmt.Sprintf("комментарий длиннее %d символов", MaxCommentLen))
	}
	return &c, nil
}

// normalizeIdempotencyKey validates an optional Idempotency-Key; blank → nil.
func normalizeIdempotencyKey(k string) (*string, error) {
	k = strings.TrimSpace(k)
	if k == "" {
		return nil, nil
	}
	if len(k) > MaxIdempotencyKeyLen {
		return nil, apperr.BadRequest("invalid_idempotency_key",
			fmt.Sprintf("Idempotency-Key длиннее %d символов", MaxIdempotencyKeyLen))
	}
	for _, r := range k {
		if r < 0x21 || r > 0x7e {
			return nil, apperr.BadRequest("invalid_idempotency_key", "Idempotency-Key должен состоять из печатных ASCII-символов")
		}
	}
	return &k, nil
}

func roundSom(v float64) float64 { return math.Round(v*100) / 100 }

// ListOrders returns customerID's own orders, newest first, each with its
// items attached.
func (s *Service) ListOrders(ctx context.Context, customerID string) ([]Order, error) {
	const q = `SELECT ` + orderColumns + ` FROM orders WHERE customer_id = $1 ORDER BY created_at DESC`

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
	q := `SELECT ` + orderColumns + ` FROM orders WHERE customer_id = $1 AND ` + orderKeyPredicate(orderID, 2)

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
	var zoneID, zoneRu, zoneKy sql.NullString
	if err := row.Scan(&o.ID, &o.OrderNumber, &o.CustomerID, &o.AddressID, &o.PointID,
		&o.Status, &o.PaymentMethod, &o.PaymentStatus, &o.TotalAmount, &o.DeliveryFee, &o.RefundRequired,
		&o.Comment, &o.CreatedAt, &o.UpdatedAt, &zoneID, &zoneRu, &zoneKy); err != nil {
		return err
	}
	o.DeliveryZone = nil
	if zoneID.Valid {
		o.DeliveryZone = &OrderDeliveryZone{ID: zoneID.String, NameRu: zoneRu.String, NameKy: zoneKy.String}
	}
	return nil
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
		// A malformed id would otherwise reach Postgres as an invalid uuid
		// literal and surface as a 500.
		if uuid.Validate(it.VariantID) != nil {
			return nil, nil, apperr.BadRequest("invalid_variant_id", "некорректный идентификатор варианта товара")
		}
		if it.Quantity <= 0 {
			return nil, nil, apperr.BadRequest("invalid_qty", "количество должно быть больше нуля")
		}
		if it.Quantity > MaxCartQty {
			return nil, nil, apperr.BadRequest("qty_too_large", fmt.Sprintf("не больше %d шт. одного товара в заказе", MaxCartQty))
		}
		merged[it.VariantID] += it.Quantity
		if merged[it.VariantID] > MaxCartQty {
			return nil, nil, apperr.BadRequest("qty_too_large", fmt.Sprintf("не больше %d шт. одного товара в заказе", MaxCartQty))
		}
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
// doesn't exist; an inactive product comes back with Active=false so the
// caller can name it in the error.
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
		SELECT pv.id, pv.size, pv.color, pv.price_override, p.name_ru, p.base_price, p.is_active
		FROM product_variants pv
		JOIN products p ON p.id = pv.product_id
		WHERE pv.id IN (%s)`, strings.Join(placeholders, ", "))

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
		var active bool
		if err := rows.Scan(&id, &size, &color, &override, &name, &base, &active); err != nil {
			return nil, err
		}
		price := base
		if override != nil {
			price = *override
		}
		out[id] = variantSnapshot{ProductName: name, Size: size, Color: color, Price: price, Active: active}
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
func pickFulfillmentPoint(ctx context.Context, tx *sql.Tx, variantIDs []string, qtyByVariant map[string]int, snapshots map[string]variantSnapshot) (string, error) {
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
	return "", insufficientStockForDelivery(ctx, tx, variantIDs, qtyByVariant, snapshots)
}

// insufficientStockForDelivery builds the error for "no single point can
// fulfill the whole delivery order", naming the first line that no active
// point has enough of — or, if every line exists somewhere, saying the
// order would have to be split.
func insufficientStockForDelivery(ctx context.Context, tx *sql.Tx, variantIDs []string, qtyByVariant map[string]int, snapshots map[string]variantSnapshot) error {
	const q = `
		SELECT COALESCE(MAX(s.quantity), 0)
		FROM stock s JOIN points_of_sale p ON p.id = s.point_id AND p.is_active = true
		WHERE s.variant_id = $1`
	for _, variantID := range variantIDs {
		var best int
		if err := tx.QueryRowContext(ctx, q, variantID).Scan(&best); err != nil {
			return err
		}
		if best < qtyByVariant[variantID] {
			return stockError(snapshots[variantID], best)
		}
	}
	return apperr.Conflict("insufficient_stock",
		"весь заказ целиком нет ни в одном магазине — оформите товары отдельными заказами или выберите самовывоз")
}

// stockError names the line that is short: "Air Max, 42 — осталось 1 шт.".
func stockError(snap variantSnapshot, available int) error {
	if available <= 0 {
		return apperr.Conflict("insufficient_stock", fmt.Sprintf("товар «%s» закончился", snap.label()))
	}
	return apperr.Conflict("insufficient_stock",
		fmt.Sprintf("товара «%s» осталось только %d шт.", snap.label(), available))
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
func lockAndDecrementStock(ctx context.Context, tx *sql.Tx, pointID string, variantIDs []string, qtyByVariant map[string]int, snapshots map[string]variantSnapshot) error {
	const lockQ = `SELECT quantity FROM stock WHERE variant_id = $1 AND point_id = $2 FOR UPDATE`
	const updQ = `UPDATE stock SET quantity = quantity - $1, updated_at = now() WHERE variant_id = $2 AND point_id = $3`

	for _, variantID := range variantIDs {
		var qty int
		err := tx.QueryRowContext(ctx, lockQ, variantID, pointID).Scan(&qty)
		if errors.Is(err, sql.ErrNoRows) {
			return stockError(snapshots[variantID], 0)
		}
		if err != nil {
			return err
		}

		need := qtyByVariant[variantID]
		if qty < need {
			return stockError(snapshots[variantID], qty)
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
