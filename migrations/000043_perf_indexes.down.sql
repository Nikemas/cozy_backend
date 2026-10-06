DROP INDEX IF EXISTS idx_customers_phone_digits_trgm;
DROP INDEX IF EXISTS idx_orders_number_trgm;
DROP INDEX IF EXISTS idx_product_variants_product_price;
DROP INDEX IF EXISTS idx_product_variants_sku_trgm;
DROP EXTENSION IF EXISTS pg_trgm;
