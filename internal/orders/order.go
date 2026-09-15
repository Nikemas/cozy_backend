package orders

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
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

// PaymentMethod mirrors the payment_method enum. Bakai/online payment is
// out of scope for this MVP web slice (see web-plan Architecture
// Decisions) — the checkout flow only ever creates cash_on_delivery
// orders for now.
type PaymentMethod string

const (
	PaymentOnline         PaymentMethod = "online"
	PaymentCashOnDelivery PaymentMethod = "cash_on_delivery"
)

// Order mirrors one row of orders (+ its order_items).
type Order struct {
	ID            string
	OrderNumber   string // "COZY-YYYYMMDD-NNN"
	CustomerID    string
	AddressID     *string
	PointID       *string
	Status        OrderStatus
	PaymentMethod PaymentMethod
	TotalAmount   float64
	Comment       *string
	Items         []OrderItem
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// OrderItem mirrors one row of order_items — a price/size/color snapshot
// taken at checkout time, independent of later catalog edits.
type OrderItem struct {
	ID                  string
	OrderID             string
	VariantID           string
	ProductNameSnapshot string
	SizeSnapshot        string
	ColorSnapshot       string
	Quantity            int
	Price               float64
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
	db *sql.DB
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// CreateOrder creates an order from items, atomically decrementing stock
// (a transaction with SELECT ... FOR UPDATE against catalog.Stock, per
// the tech spec's withTx pattern) and snapshotting price/size/color into
// order_items. Exactly one of addressID/pickupPointID is expected to be
// set — delivery vs. self-pickup.
func (s *Service) CreateOrder(ctx context.Context, customerID string, items []OrderItemInput, addressID, pickupPointID *string) (*Order, error) {
	return nil, apperr.New(http.StatusNotImplemented, "not_implemented", "orders.Service.CreateOrder: not implemented")
}

// ListOrders returns customerID's own orders, newest first.
func (s *Service) ListOrders(ctx context.Context, customerID string) ([]Order, error) {
	return nil, apperr.New(http.StatusNotImplemented, "not_implemented", "orders.Service.ListOrders: not implemented")
}

// GetOrder returns one order, scoped to customerID so a customer can't
// fetch someone else's order by guessing an ID.
func (s *Service) GetOrder(ctx context.Context, customerID, orderID string) (*Order, error) {
	return nil, apperr.New(http.StatusNotImplemented, "not_implemented", "orders.Service.GetOrder: not implemented")
}
