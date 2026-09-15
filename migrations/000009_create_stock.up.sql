CREATE TABLE stock (
  variant_id    UUID NOT NULL REFERENCES product_variants(id) ON DELETE CASCADE,
  point_id      UUID NOT NULL REFERENCES points_of_sale(id) ON DELETE CASCADE,
  quantity      INT NOT NULL DEFAULT 0 CHECK (quantity >= 0),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (variant_id, point_id)
);
