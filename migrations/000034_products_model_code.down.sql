DROP INDEX IF EXISTS idx_products_model_code;
ALTER TABLE products DROP COLUMN IF EXISTS model_code;
