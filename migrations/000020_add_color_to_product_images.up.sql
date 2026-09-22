-- A photo today belongs only to a product as a whole (product_id), so the
-- storefront shows the same gallery no matter which color variant the
-- customer selects. This adds an optional color tag: NULL keeps meaning
-- "general product photo" (unchanged existing rows), a non-NULL value ties
-- the photo to that exact product_variants.color string so staff can attach
-- black-shoe photos to "Черный" and white-shoe photos to "Белый" on the
-- same product.
ALTER TABLE product_images ADD COLUMN color TEXT;

CREATE INDEX idx_product_images_product_color ON product_images (product_id, color);
