-- fix/orders-integrity: money, idempotency and refund bookkeeping on orders,
-- plus what the pending-payment expiry job needs.

-- 1. delivery_fee: the delivery charge included in total_amount (0 for
--    self-pickup and for every order placed before this migration — those
--    totals never included delivery). total_amount stays the amount the
--    customer owes/pays, delivery included.
ALTER TABLE orders ADD COLUMN delivery_fee NUMERIC(10,2) NOT NULL DEFAULT 0
  CONSTRAINT orders_delivery_fee_chk CHECK (delivery_fee >= 0);

-- 2. refund_required: money was (or may have been) taken for an order that
--    ended up cancelled — staff must refund it through the bank. Set when a
--    paid online order is cancelled, or when a "paid" callback arrives for
--    an order already cancelled/expired.
ALTER TABLE orders ADD COLUMN refund_required BOOLEAN NOT NULL DEFAULT false;

-- 3. idempotency_key: client-supplied Idempotency-Key of POST
--    /api/v1/orders (and the site checkout form token). A retry with the
--    same key by the same customer returns the existing order instead of
--    creating a second one. Keys are honoured for 24h; the service clears
--    an older key before reusing it, the unique index is the backstop
--    against a concurrent double submit.
ALTER TABLE orders ADD COLUMN idempotency_key TEXT
  CONSTRAINT orders_idempotency_key_len_chk CHECK (char_length(idempotency_key) BETWEEN 1 AND 64);
CREATE UNIQUE INDEX orders_customer_idempotency_uniq ON orders (customer_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;

-- 4. Open-orders limit per customer counts non-final orders by customer.
CREATE INDEX idx_orders_customer_open ON orders (customer_id)
  WHERE status IN ('placed', 'confirmed', 'courier_assigned');

-- 5. payments.redirect_url: the provider checkout URL, so an idempotent
--    replay of an online order can hand the same payment_url back.
ALTER TABLE payments ADD COLUMN redirect_url TEXT;

-- 6. The expiry job scans only pending payments by age.
CREATE INDEX idx_payments_pending_created ON payments (created_at)
  WHERE status = 'pending';
