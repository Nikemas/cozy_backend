CREATE TYPE order_status AS ENUM (
  'placed', 'confirmed', 'courier_assigned', 'delivered', 'cancelled'
);
CREATE TYPE payment_method AS ENUM ('online', 'cash_on_delivery');

CREATE TABLE orders (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  order_number  TEXT NOT NULL UNIQUE,     -- COZY-20260914-001
  customer_id   UUID NOT NULL REFERENCES customers(id),
  address_id    UUID NOT NULL REFERENCES customer_addresses(id),
  point_id      UUID REFERENCES points_of_sale(id), -- откуда комплектуется
  status        order_status NOT NULL DEFAULT 'placed',
  payment_method payment_method NOT NULL,
  total_amount  NUMERIC(10,2) NOT NULL,
  comment       TEXT,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_orders_customer ON orders(customer_id);
CREATE INDEX idx_orders_status   ON orders(status);
CREATE INDEX idx_orders_point    ON orders(point_id);
