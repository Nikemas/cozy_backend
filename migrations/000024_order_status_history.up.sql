-- fix/orders-integrity: audit trail of order status changes. One row per
-- transition (and one 'placed' row per new order), written in the same
-- transaction as the status change itself.
CREATE TABLE order_status_history (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id       UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  from_status    order_status,               -- NULL for the initial 'placed' row
  to_status      order_status NOT NULL,
  actor_type     TEXT NOT NULL
    CONSTRAINT order_status_history_actor_chk CHECK (actor_type IN ('staff', 'customer', 'system')),
  actor_staff_id UUID REFERENCES staff(id) ON DELETE SET NULL,
  note           TEXT,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_order_status_history_order ON order_status_history (order_id, created_at);

-- Backfill: every existing order gets its creation row, and orders that
-- already moved on get one "current status" row (the intermediate steps
-- were never recorded, so only the latest one is known).
INSERT INTO order_status_history (order_id, from_status, to_status, actor_type, note, created_at)
SELECT id, NULL, 'placed', 'system', 'backfill', created_at FROM orders;
INSERT INTO order_status_history (order_id, from_status, to_status, actor_type, note, created_at)
SELECT id, NULL, status, 'system', 'backfill', updated_at FROM orders WHERE status <> 'placed';
