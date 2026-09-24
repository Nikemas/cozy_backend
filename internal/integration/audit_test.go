//go:build integration

package integration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/dbtx"
	"github.com/Nikemas/cozy_backend/internal/httpmw"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// newStaff inserts an active staff member with the given role.
func newStaff(t *testing.T, role staff.Role) *staff.Staff {
	t.Helper()
	st := &staff.Staff{Phone: "+996555" + uuid.NewString()[:6], Name: "Staff " + uuid.NewString()[:4], Role: role, IsActive: true}
	if err := testDB.QueryRowContext(ctxT(t),
		`INSERT INTO staff (phone, password_hash, name, role) VALUES ($1, 'x', $2, $3) RETURNING id`,
		st.Phone, st.Name, string(role)).Scan(&st.ID); err != nil {
		t.Fatalf("insert staff: %v", err)
	}
	return st
}

func TestAuditJournal(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 1, 1)
	owner := newStaff(t, staff.RoleOwner)
	actx := httpmw.WithClientIP(staff.NewContextWithStaff(ctx, owner), "198.51.100.4")
	log := audit.New(testDB)

	log.Record(actx, audit.Entry{
		Action: audit.ActionProductUpdate, EntityType: audit.EntityProduct, EntityID: f.ProductID,
		Summary: "Товар изменён", Details: map[string]any{"base_price": audit.Change{From: 2500, To: 2700}},
	})

	// Inside a transaction: a journal insert that fails (unknown staff id
	// -> FK violation) must not abort the caller's own write.
	ghost := &staff.Staff{ID: uuid.NewString(), Role: staff.RoleOwner}
	err := dbtx.WithTx(ctx, testDB, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE products SET base_price = 2700 WHERE id = $1`, f.ProductID); err != nil {
			return err
		}
		log.RecordTx(staff.NewContextWithStaff(ctx, ghost), tx, audit.Entry{Action: audit.ActionProductUpdate, EntityType: audit.EntityProduct})
		log.RecordTx(actx, tx, audit.Entry{Action: audit.ActionStockUpdate, EntityType: audit.EntityStock, EntityID: f.VariantA,
			Details: map[string]any{"quantity": audit.Change{From: 1, To: 3}}})
		_, err := tx.ExecContext(ctx, `UPDATE stock SET quantity = 3 WHERE variant_id = $1 AND point_id = $2`, f.VariantA, f.PointA)
		return err
	})
	if err != nil {
		t.Fatalf("tx with a failed journal write: %v", err)
	}
	var price float64
	if err := testDB.QueryRowContext(ctx, `SELECT base_price FROM products WHERE id = $1`, f.ProductID).Scan(&price); err != nil || price != 2700 {
		t.Fatalf("product price = %v (%v), want 2700 committed", price, err)
	}
	if got := stockQty(t, f.VariantA, f.PointA); got != 3 {
		t.Fatalf("stock = %d, want 3 committed", got)
	}

	// An order status change by this owner, recorded only in
	// order_status_history — the journal must show it without a copy.
	orderNumber := "IT-" + uuid.NewString()[:8]
	var orderID string
	if err := testDB.QueryRowContext(ctx,
		`INSERT INTO orders (order_number, customer_id, point_id, status, payment_method, total_amount)
		 VALUES ($1, $2, $3, 'confirmed', 'cash_on_delivery', 2500) RETURNING id`,
		orderNumber, f.CustomerID, f.PointA).Scan(&orderID); err != nil {
		t.Fatalf("insert order: %v", err)
	}
	if _, err := testDB.ExecContext(ctx,
		`INSERT INTO order_status_history (order_id, from_status, to_status, actor_type, actor_staff_id, created_at)
		 VALUES ($1, 'placed', 'confirmed', 'staff', $2, now() + interval '1 second')`, orderID, owner.ID); err != nil {
		t.Fatalf("insert history: %v", err)
	}

	list := func(f audit.Filter) ([]audit.Row, int) {
		t.Helper()
		f.StaffID = owner.ID
		rows, total, err := log.List(context.Background(), f)
		if err != nil {
			t.Fatalf("List(%+v): %v", f, err)
		}
		return rows, total
	}

	rows, total := list(audit.Filter{})
	if total != 3 || len(rows) != 3 {
		t.Fatalf("owner's journal: total=%d rows=%d, want 3 (2 audit_log + 1 order status)", total, len(rows))
	}
	if rows[0].Action != audit.ActionOrderStatus || rows[0].EntityID != orderID || rows[0].Details["to"] != "confirmed" ||
		rows[0].StaffName != owner.Name {
		t.Errorf("newest row = %+v, want the order status change", rows[0])
	}
	var stockRow *audit.Row
	for i := range rows {
		if rows[i].Action == audit.ActionStockUpdate {
			stockRow = &rows[i]
		}
	}
	if stockRow == nil || stockRow.IP != "198.51.100.4" {
		t.Errorf("stock row = %+v, want IP recorded", stockRow)
	}

	if _, total := list(audit.Filter{EntityType: audit.EntityOrder}); total != 1 {
		t.Errorf("entity=order total = %d, want 1", total)
	}
	if _, total := list(audit.Filter{EntityQuery: orderNumber}); total != 1 {
		t.Errorf("search by order number total = %d, want 1", total)
	}
	if _, total := list(audit.Filter{EntityQuery: f.ProductID[:8]}); total != 1 {
		t.Errorf("search by product id prefix total = %d, want 1", total)
	}
	if _, total := list(audit.Filter{From: time.Now().Add(time.Hour)}); total != 0 {
		t.Errorf("future range total = %d, want 0", total)
	}
	if rows, total := list(audit.Filter{PageSize: 2, Page: 2}); total != 3 || len(rows) != 1 {
		t.Errorf("page 2 of 2: total=%d rows=%d", total, len(rows))
	}
}
