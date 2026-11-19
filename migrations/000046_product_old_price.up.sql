-- Discount display: products.old_price is the price "before the discount".
-- When set, the storefront (web + mobile) shows the current price in green
-- and old_price in red, struck through. NULL = no discount. Orders are
-- unaffected: they are always placed at the current (effective) price.
-- The admin form additionally requires old_price > base_price; the DB only
-- guards against non-positive values so a later base_price edit can't make
-- an existing row invalid (the storefront hides old_price that isn't above
-- the price it is showing).

-- ADD COLUMN takes ACCESS EXCLUSIVE on products; fail fast during a deploy
-- instead of queueing behind long catalog reads.
SET lock_timeout = '10s';

ALTER TABLE products
  ADD COLUMN old_price NUMERIC(10,2) NULL
  CONSTRAINT products_old_price_positive CHECK (old_price IS NULL OR old_price > 0);
