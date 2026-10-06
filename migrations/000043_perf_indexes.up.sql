-- perf/db-at-scale: indexes for the hot read paths measured in
-- docs/performance.md (5k products / 60k variants / 100k orders scale).
--
-- Plain CREATE INDEX, not CONCURRENTLY: golang-migrate runs each file in a
-- transaction, where CONCURRENTLY isn't allowed. On tables of this size the
-- build takes well under a second; the write lock it holds meanwhile is
-- harmless for the shop. If a table ever grows large enough for that to
-- matter, build the index by hand with CONCURRENTLY first — IF NOT EXISTS
-- then makes this file a no-op for it.

-- Trigram indexes serve ILIKE '%term%' (the catalog/admin search and the
-- admin orders search). pg_trgm ships with Postgres and is a trusted
-- extension (PG13+), so the database owner may create it.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- catalog search (catalog.searchCondition): every token also matches any
-- variant SKU — "EXISTS (... pv.sku ILIKE $n)", which Postgres runs as one
-- hashed subplan over product_variants; this turns that from a scan of
-- every variant into an index lookup.
CREATE INDEX IF NOT EXISTS idx_product_variants_sku_trgm
  ON product_variants USING gin (sku gin_trgm_ops);

-- Effective price (catalog minPriceExpr/maxPriceExpr): a correlated
-- MIN/MAX(COALESCE(price_override, base_price)) over a product's variants
-- for every product when sorting by price or filtering by a price range.
-- With price_override in the index those lookups are index-only scans
-- instead of heap visits (~3x faster sort=price_asc).
CREATE INDEX IF NOT EXISTS idx_product_variants_product_price
  ON product_variants (product_id) INCLUDE (price_override);

-- Admin orders search (internal/admin/orders_search.go): order number
-- substring, and the customer's phone digits.
CREATE INDEX IF NOT EXISTS idx_orders_number_trgm
  ON orders USING gin (order_number gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_customers_phone_digits_trgm
  ON customers USING gin ((regexp_replace(phone, '[^0-9]', '', 'g')) gin_trgm_ops);
