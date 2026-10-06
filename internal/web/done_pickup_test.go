package web

import (
	"testing"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

// Pickup is decided by the order having a pickup point and no delivery
// address — not by a missing address alone.
func TestIsPickupOrder(t *testing.T) {
	addr, point := "a1", "p1"
	cases := []struct {
		name  string
		order orders.Order
		want  bool
	}{
		{"pickup", orders.Order{PointID: &point}, true},
		{"delivery", orders.Order{AddressID: &addr}, false},
		{"neither", orders.Order{}, false},
		{"both", orders.Order{AddressID: &addr, PointID: &point}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPickupOrder(&c.order); got != c.want {
				t.Errorf("isPickupOrder = %v, want %v", got, c.want)
			}
		})
	}
}
