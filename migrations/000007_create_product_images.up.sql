CREATE TABLE product_images (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  product_id    UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  object_key    TEXT NOT NULL,            -- ключ в MinIO
  sort_order    INT NOT NULL DEFAULT 0
);
