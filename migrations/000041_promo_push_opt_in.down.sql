ALTER TABLE customers
  DROP COLUMN IF EXISTS promo_push_asked_at,
  ALTER COLUMN promo_push SET DEFAULT true;
