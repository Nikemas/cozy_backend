-- Model article ("артикул модели") for the xlsx catalog import: one product
-- per model, re-import matches the product by this code instead of creating
-- a duplicate. NULL for products created by hand in the admin form.
ALTER TABLE products ADD COLUMN model_code TEXT;

CREATE UNIQUE INDEX idx_products_model_code
  ON products (lower(model_code))
  WHERE model_code IS NOT NULL;
