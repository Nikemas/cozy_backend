DROP INDEX IF EXISTS idx_orders_delivery_zone;
ALTER TABLE orders DROP COLUMN IF EXISTS delivery_zone_id;
DROP TABLE IF EXISTS delivery_zones;
