SET lock_timeout = '10s';

ALTER TABLE orders
  DROP COLUMN IF EXISTS delivery_address_label,
  DROP COLUMN IF EXISTS delivery_address_text;
