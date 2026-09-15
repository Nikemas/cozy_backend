ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_address_or_point_chk;
ALTER TABLE orders ALTER COLUMN address_id SET NOT NULL;
