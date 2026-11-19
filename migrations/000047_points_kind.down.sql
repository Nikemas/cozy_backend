ALTER TABLE points_of_sale
  DROP CONSTRAINT IF EXISTS points_of_sale_kind_chk,
  DROP COLUMN IF EXISTS kind;
