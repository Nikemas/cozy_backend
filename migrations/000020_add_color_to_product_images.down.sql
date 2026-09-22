DROP INDEX IF EXISTS idx_product_images_product_color;
ALTER TABLE product_images DROP COLUMN IF EXISTS color;
