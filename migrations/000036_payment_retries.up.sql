-- fix/checkout-payments: retrying an online payment on the same order
-- (POST /api/v1/orders/{id}/pay). Each attempt is its own payments row; the
-- previous pending one is closed ('cancelled') when a new one is opened.

-- 1. At most one pending payment per order — the backstop for two retries
--    racing each other. Close any older duplicates first (keep the newest).
UPDATE payments p SET status = 'cancelled', updated_at = now()
WHERE p.status = 'pending'
  AND EXISTS (
    SELECT 1 FROM payments q
    WHERE q.order_id = p.order_id AND q.status = 'pending'
      AND (q.created_at, q.id) > (p.created_at, p.id));
CREATE UNIQUE INDEX payments_one_pending_per_order ON payments (order_id) WHERE status = 'pending';

-- 2. The expiry job now scans open, unpaid online orders (pending or failed
--    latest payment) rather than pending payment rows.
CREATE INDEX idx_orders_online_unpaid ON orders (created_at)
  WHERE status = 'placed' AND payment_method = 'online_card' AND payment_status IN ('pending', 'failed');
