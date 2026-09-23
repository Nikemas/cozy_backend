-- Task S: online card payments (Bakai acquiring, mock provider until the
-- bank grants access). 000012_create_payments already created the payments
-- table and 000010_create_orders already reserved an 'online' value in the
-- payment_method enum, so this migration extends both instead of creating
-- anything from scratch.

-- 1. payment_method: 'online' -> 'online_card'. Nothing ever wrote 'online'
--    (checkout only created cash_on_delivery orders), so a RENAME VALUE is
--    safe, transactional, and — unlike ALTER TYPE ... ADD VALUE — cleanly
--    reversible in the down migration.
ALTER TYPE payment_method RENAME VALUE 'online' TO 'online_card';

-- 2. payment_status gains 'cancelled' (customer abandoned/cancelled on the
--    bank page, as opposed to 'failed' = declined). The type is recreated
--    rather than extended with ADD VALUE so the down migration can drop the
--    value again (Postgres has no ALTER TYPE ... DROP VALUE).
CREATE TYPE payment_status_v2 AS ENUM ('pending', 'paid', 'failed', 'cancelled', 'refunded');
ALTER TABLE payments ALTER COLUMN status DROP DEFAULT;
ALTER TABLE payments ALTER COLUMN status TYPE payment_status_v2 USING status::text::payment_status_v2;
ALTER TABLE payments ALTER COLUMN status SET DEFAULT 'pending';
DROP TYPE payment_status;
ALTER TYPE payment_status_v2 RENAME TO payment_status;

-- 3. payments: currency (KGS only for now), lookup by the provider's own
--    transaction id (provider_tx_id is the "external id"), and the unique
--    index that makes webhook handling idempotent per provider.
ALTER TABLE payments ADD COLUMN currency TEXT NOT NULL DEFAULT 'KGS'
  CONSTRAINT payments_currency_chk CHECK (currency = 'KGS');
CREATE UNIQUE INDEX payments_provider_tx_uniq ON payments (provider, provider_tx_id)
  WHERE provider_tx_id IS NOT NULL;
CREATE INDEX idx_payments_order ON payments (order_id);

-- 4. orders.payment_status: denormalized current payment state of an
--    online_card order (NULL for cash_on_delivery — paid in cash on
--    delivery/pickup, not tracked here). Updated in the same transaction as
--    the payments row by the webhook handler.
ALTER TABLE orders ADD COLUMN payment_status payment_status;
-- Backfill (only matters when re-applying after a down migration with
-- online orders already present): latest payment row, else pending.
UPDATE orders o SET payment_status = COALESCE(
  (SELECT p.status FROM payments p WHERE p.order_id = o.id ORDER BY p.created_at DESC LIMIT 1),
  'pending')
WHERE o.payment_method = 'online_card';
ALTER TABLE orders ADD CONSTRAINT orders_payment_status_chk
  CHECK ((payment_method = 'online_card') = (payment_status IS NOT NULL));
