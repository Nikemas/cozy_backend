ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_payment_status_chk;
ALTER TABLE orders DROP COLUMN IF EXISTS payment_status;

DROP INDEX IF EXISTS idx_payments_order;
DROP INDEX IF EXISTS payments_provider_tx_uniq;
ALTER TABLE payments DROP COLUMN IF EXISTS currency;

-- 'cancelled' has no equivalent in the old enum; fold it into 'failed'.
CREATE TYPE payment_status_v1 AS ENUM ('pending', 'paid', 'failed', 'refunded');
ALTER TABLE payments ALTER COLUMN status DROP DEFAULT;
ALTER TABLE payments ALTER COLUMN status TYPE payment_status_v1
  USING (CASE WHEN status::text = 'cancelled' THEN 'failed' ELSE status::text END)::payment_status_v1;
ALTER TABLE payments ALTER COLUMN status SET DEFAULT 'pending';
DROP TYPE payment_status;
ALTER TYPE payment_status_v1 RENAME TO payment_status;

ALTER TYPE payment_method RENAME VALUE 'online_card' TO 'online';
