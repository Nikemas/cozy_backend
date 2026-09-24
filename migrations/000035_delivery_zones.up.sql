-- fix/checkout-payments: delivery zones. A delivery order's fee comes from
-- the zone the customer picks (fee, free above free_from). While no zone is
-- active the flat DELIVERY_FEE_SOM keeps applying (GET /api/v1/app/config's
-- delivery_fee), so the shop can switch zones on without a deploy.
CREATE TABLE delivery_zones (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name_ru    TEXT NOT NULL
    CONSTRAINT delivery_zones_name_ru_chk CHECK (char_length(btrim(name_ru)) BETWEEN 1 AND 100),
  name_ky    TEXT NOT NULL
    CONSTRAINT delivery_zones_name_ky_chk CHECK (char_length(btrim(name_ky)) BETWEEN 1 AND 100),
  fee        NUMERIC(10,2) NOT NULL
    CONSTRAINT delivery_zones_fee_chk CHECK (fee >= 0 AND fee <= 100000),
  -- Items total (som) from which delivery to this zone is free; NULL = never.
  free_from  NUMERIC(12,2)
    CONSTRAINT delivery_zones_free_from_chk CHECK (free_from IS NULL OR (free_from > 0 AND free_from <= 10000000)),
  is_active  BOOLEAN NOT NULL DEFAULT true,
  sort_order INT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_delivery_zones_active_sort ON delivery_zones (sort_order, name_ru) WHERE is_active;

-- The zone a delivery order was charged for (NULL: pickup, or placed before
-- zones / while none was active). RESTRICT: a zone used by orders can only
-- be deactivated, not deleted.
ALTER TABLE orders ADD COLUMN delivery_zone_id UUID REFERENCES delivery_zones(id) ON DELETE RESTRICT;
CREATE INDEX idx_orders_delivery_zone ON orders (delivery_zone_id) WHERE delivery_zone_id IS NOT NULL;
