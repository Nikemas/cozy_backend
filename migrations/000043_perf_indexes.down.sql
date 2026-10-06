DROP INDEX IF EXISTS idx_customers_phone_digits_trgm;
DROP INDEX IF EXISTS idx_orders_number_trgm;
DROP INDEX IF EXISTS idx_product_variants_product_price;
DROP INDEX IF EXISTS idx_product_variants_sku_trgm;
-- pg_trgm is left installed on purpose: it may predate this migration
-- (created by hand or another tool), and dropping it would break whatever
-- else uses it. It is harmless to keep.
