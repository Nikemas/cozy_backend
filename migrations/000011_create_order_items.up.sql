CREATE TABLE order_items (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id      UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  variant_id    UUID NOT NULL REFERENCES product_variants(id),
  product_name_snapshot TEXT NOT NULL,    -- денормализовано на момент заказа
  size_snapshot TEXT NOT NULL,
  color_snapshot TEXT NOT NULL,
  quantity      INT NOT NULL CHECK (quantity > 0),
  price         NUMERIC(10,2) NOT NULL    -- цена на момент заказа
);
