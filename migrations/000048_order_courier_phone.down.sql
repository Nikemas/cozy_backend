SET lock_timeout = '10s';

ALTER TABLE orders
  DROP COLUMN IF EXISTS courier_phone;
