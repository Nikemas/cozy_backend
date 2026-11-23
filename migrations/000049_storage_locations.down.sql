DROP INDEX IF EXISTS stock_location_idx;
ALTER TABLE stock
  DROP CONSTRAINT IF EXISTS stock_location_fk,
  DROP COLUMN IF EXISTS location_id;
DROP TABLE IF EXISTS storage_locations;
