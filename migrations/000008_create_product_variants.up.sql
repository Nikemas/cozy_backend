CREATE TABLE product_variants (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  product_id    UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  size          TEXT NOT NULL,            -- "42", "M" и т.д.
  color         TEXT NOT NULL,
  sku           TEXT UNIQUE,
  price_override NUMERIC(10,2),           -- NULL = берём base_price
  UNIQUE (product_id, size, color)
);
