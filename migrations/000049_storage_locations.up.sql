SET lock_timeout = '10s';

-- Storage locations (места хранения) inside a warehouse: "Стеллаж A,
-- полка 3". Only points_of_sale.kind = 'warehouse' gets them — checked by
-- the app (internal/points.LocationsRepo), since a CHECK can't look at
-- another table. Names are unique per warehouse, case-insensitively.
CREATE TABLE storage_locations (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  point_id    UUID NOT NULL REFERENCES points_of_sale(id) ON DELETE CASCADE,
  name        TEXT NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 100),
  sort_order  INT NOT NULL DEFAULT 0,
  is_active   BOOLEAN NOT NULL DEFAULT true,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- target of stock's composite FK below: a stock row may only point at a
  -- location of its own point
  CONSTRAINT storage_locations_id_point_uq UNIQUE (id, point_id)
);

CREATE UNIQUE INDEX storage_locations_point_name_uq ON storage_locations (point_id, lower(name));

-- A stock row (variant × point) may say where in the warehouse it lies.
-- Optional; deleting the location clears only location_id (PG15+ column
-- list), never the row's point_id.
ALTER TABLE stock
  ADD COLUMN location_id UUID,
  ADD CONSTRAINT stock_location_fk FOREIGN KEY (location_id, point_id)
    REFERENCES storage_locations (id, point_id) ON DELETE SET NULL (location_id);

CREATE INDEX stock_location_idx ON stock (location_id) WHERE location_id IS NOT NULL;
