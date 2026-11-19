SET lock_timeout = '10s';
ALTER TABLE products DROP COLUMN IF EXISTS old_price;
