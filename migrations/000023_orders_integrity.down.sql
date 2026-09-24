DROP INDEX IF EXISTS idx_payments_pending_created;
ALTER TABLE payments DROP COLUMN IF EXISTS redirect_url;
DROP INDEX IF EXISTS idx_orders_customer_open;
DROP INDEX IF EXISTS orders_customer_idempotency_uniq;
ALTER TABLE orders DROP COLUMN IF EXISTS idempotency_key;
ALTER TABLE orders DROP COLUMN IF EXISTS refund_required;
ALTER TABLE orders DROP COLUMN IF EXISTS delivery_fee;
