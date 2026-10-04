-- Points of sale: structured address + opening hours + coordinates so the
-- branch can be linked/embedded in 2GIS (https://2gis.kg/geo/<lon>,<lat>).
-- address stays the street line only ("ул. Дордой, 1"); the city is its own
-- column. latitude/longitude are optional but come as a pair.
ALTER TABLE points_of_sale
  ADD COLUMN city          TEXT NOT NULL DEFAULT 'Бишкек',
  ADD COLUMN working_hours TEXT NOT NULL DEFAULT '',
  ADD COLUMN latitude      DOUBLE PRECISION,
  ADD COLUMN longitude     DOUBLE PRECISION,
  ADD CONSTRAINT points_of_sale_geo_chk CHECK (
    (latitude IS NULL AND longitude IS NULL)
    OR (latitude BETWEEN -90 AND 90 AND longitude BETWEEN -180 AND 180)
  );
