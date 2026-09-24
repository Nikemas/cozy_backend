//go:build integration

package integration

import (
	"context"
	"database/sql"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

// The two orders APIs below are being reshaped on the fix/orders-integrity
// branch (CreateOnlineOrder takes a PlaceOrderInput and also returns
// `created`; CancelUnpaidOrderTx takes a history note). The tests call them
// only through these adapters, so after that merge only this file changes:
//
//	o, paymentID, _, err := svc.CreateOnlineOrder(ctx, orders.PlaceOrderInput{
//		CustomerID: customerID, Items: items, AddressID: addressID, PickupPointID: pickupPointID,
//	}, provider)
//	return o, paymentID, err
//
//	return orders.CancelUnpaidOrderTx(ctx, tx, orderID, "integration test")

func createOnlineOrder(ctx context.Context, svc *orders.Service, customerID string, items []orders.OrderItemInput,
	addressID, pickupPointID *string, provider string) (*orders.Order, string, error) {
	o, paymentID, _, err := svc.CreateOnlineOrder(ctx, orders.PlaceOrderInput{
		CustomerID: customerID, Items: items, AddressID: addressID, PickupPointID: pickupPointID,
	}, provider)
	return o, paymentID, err
}

func cancelUnpaidOrderTx(ctx context.Context, tx *sql.Tx, orderID string) (bool, error) {
	return orders.CancelUnpaidOrderTx(ctx, tx, orderID, "integration test")
}
