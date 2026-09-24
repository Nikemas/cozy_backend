-- W5 fix/admin-ops: journal of staff actions in the admin panel (products,
-- variants, stock, categories, points of sale, staff accounts). Order status
-- changes are NOT duplicated here: they already live in
-- order_status_history (000024) and the journal page reads them from there.
CREATE TABLE audit_log (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  staff_id    UUID REFERENCES staff(id) ON DELETE SET NULL,
  action      TEXT NOT NULL,              -- e.g. product.update, stock.update
  entity_type TEXT NOT NULL,              -- product, variant, stock, category, point, staff
  entity_id   TEXT NOT NULL DEFAULT '',
  summary     TEXT NOT NULL DEFAULT '',
  details     JSONB NOT NULL DEFAULT '{}'::jsonb,
  ip          TEXT
);
CREATE INDEX idx_audit_log_at ON audit_log (at DESC);
CREATE INDEX idx_audit_log_staff_at ON audit_log (staff_id, at DESC);
CREATE INDEX idx_audit_log_entity ON audit_log (entity_type, entity_id);

-- The journal also lists staff-made order status changes from
-- order_status_history, newest first.
CREATE INDEX idx_order_status_history_staff_at ON order_status_history (created_at DESC)
  WHERE actor_type = 'staff';
