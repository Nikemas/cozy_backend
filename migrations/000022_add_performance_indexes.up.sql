-- Task U: indexes for query patterns the code actually runs that no
-- existing index (PK / UNIQUE / earlier CREATE INDEX) serves. Audited
-- against every SQL string in internal/** — see tasks/todo.md Task U for
-- the full list of what was checked and deliberately NOT indexed (e.g.
-- product_variants(product_id) is already the leading column of
-- UNIQUE (product_id, size, color); payments(order_id) is created by
-- 000021_payments_online_card).
--
-- Plain CREATE INDEX (not CONCURRENTLY): golang-migrate runs the file as
-- one multi-statement exec, where CONCURRENTLY is not allowed, and these
-- tables are small enough that the brief write lock is harmless. IF NOT
-- EXISTS keeps a re-run after a partial manual apply safe.

-- order_items has NO index besides its PK. Every order read loads items by
-- order_id (orders.Service ListOrders/GetOrder/AdminGetOrder,
-- reports.Repo.LoadOrders, the admin orders list's batch item count), and
-- ON DELETE CASCADE from orders scans it too.
CREATE INDEX IF NOT EXISTS idx_order_items_order ON order_items (order_id);

-- order_items.variant_id REFERENCES product_variants without an index, so
-- every DELETE FROM product_variants (admin variant delete) seq-scans
-- order_items for the FK RESTRICT check; also the join key for the admin
-- brand-sales report.
CREATE INDEX IF NOT EXISTS idx_order_items_variant ON order_items (variant_id);

-- Admin orders list (ORDER BY created_at DESC LIMIT/OFFSET, optional
-- created_at range) and sales reports (created_at >= $1 AND < $2) all
-- filter/sort on created_at; only customer_id/status/point_id were indexed.
CREATE INDEX IF NOT EXISTS idx_orders_created_at ON orders (created_at DESC);

-- orders.address_id REFERENCES customer_addresses: a customer deleting an
-- address (DELETE /api/v1/addresses/{id}, web /addresses) seq-scanned the
-- whole orders table for the FK check. Pickup orders have NULL address_id,
-- so a partial index keeps it small.
CREATE INDEX IF NOT EXISTS idx_orders_address ON orders (address_id) WHERE address_id IS NOT NULL;

-- orders.Service.nextOrderNumber runs
--   SELECT COUNT(*) FROM orders WHERE order_number LIKE 'COZY-YYYYMMDD-%'
-- on every checkout. The UNIQUE(order_number) btree can't serve a LIKE
-- prefix under a non-C collation (the postgres image defaults to
-- en_US.utf8), so it was a full scan; text_pattern_ops makes it a range scan.
CREATE INDEX IF NOT EXISTS idx_orders_number_prefix ON orders (order_number text_pattern_ops);

-- customer_addresses had no index on customer_id, yet every read/write is
-- scoped by it (storefront.AddressRepo List/Get/Update/Delete, the
-- "clear other defaults" UPDATE, web checkout's address picker).
CREATE INDEX IF NOT EXISTS idx_customer_addresses_customer ON customer_addresses (customer_id);

-- Public catalog (catalog.ProductRepo.List, /api/v1/products, storefront
-- grid, sitemap): WHERE is_active = true [AND category_id = $n]
-- ORDER BY created_at DESC LIMIT/OFFSET. Without a category filter (the
-- home grid, sitemap) nothing could give the order, so every page view
-- sorted the whole active catalog; this makes it a LIMIT-bounded index
-- scan. With a category filter the planner picks between this and the
-- existing idx_products_category by selectivity — a (category_id,
-- created_at) composite was tried and not adopted by the planner on a
-- 5k-product test set, so it isn't added. Partial on is_active: inactive
-- products are never listed publicly.
CREATE INDEX IF NOT EXISTS idx_products_active_created ON products (created_at DESC) WHERE is_active;

-- stock's PK is (variant_id, point_id), which can't serve point_id alone:
-- DELETE FROM points_of_sale cascades into stock by point_id.
CREATE INDEX IF NOT EXISTS idx_stock_point ON stock (point_id);

-- cart_items' PK is (customer_id, variant_id); deleting a variant cascades
-- into cart_items by variant_id alone.
CREATE INDEX IF NOT EXISTS idx_cart_items_variant ON cart_items (variant_id);
