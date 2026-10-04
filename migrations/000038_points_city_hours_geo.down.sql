ALTER TABLE points_of_sale
  DROP CONSTRAINT IF EXISTS points_of_sale_geo_chk,
  DROP COLUMN IF EXISTS longitude,
  DROP COLUMN IF EXISTS latitude,
  DROP COLUMN IF EXISTS working_hours,
  DROP COLUMN IF EXISTS city;
