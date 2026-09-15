CREATE TABLE cart_items (
  customer_id   UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
  variant_id    UUID NOT NULL REFERENCES product_variants(id) ON DELETE CASCADE,
  qty           INT NOT NULL DEFAULT 1 CHECK (qty > 0),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (customer_id, variant_id)
);
