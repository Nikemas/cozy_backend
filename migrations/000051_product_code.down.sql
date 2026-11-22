SET lock_timeout = '10s';

DROP TRIGGER IF EXISTS products_fill_model_code ON products;
DROP FUNCTION IF EXISTS products_fill_model_code();

ALTER TABLE products ALTER COLUMN model_code DROP NOT NULL;
-- Generated codes go back to "no article", as before 000051; codes typed
-- by staff or imported stay.
UPDATE products SET model_code = NULL WHERE model_code_auto;

DROP FUNCTION IF EXISTS next_product_code();
DROP SEQUENCE IF EXISTS product_code_seq;
ALTER TABLE products DROP COLUMN IF EXISTS model_code_auto;
