-- Performance dataset for Cozy ("shop at realistic growth").
--
-- Generates, on top of a freshly migrated EMPTY database:
--   5 points of sale, a 2-level category tree (4 + 28), 30 brands,
--   5,000 products (~92% active), ~60,000 variants (size x color),
--   stock rows for ~60% of variant/point pairs, ~12,000 product images,
--   30,000 customers (+ ~24,000 addresses), 100,000 orders over the last
--   2 years (growing order rate, realistic status mix), ~200,000 order
--   items (popularity skewed towards a few best sellers), payments for
--   online orders, order status history, favorites and cart rows.
--
-- Reproducible: setseed() fixes random(), and every id is derived from
-- md5(<kind>-<n>) so URLs/ids are stable between runs.
--
-- Usage (see docs/performance.md):
--   createdb cozy_perf
--   migrate -database "postgres://$(whoami)@localhost:5432/cozy_perf?sslmode=disable" -path migrations up
--   psql -v ON_ERROR_STOP=1 -d cozy_perf -f scripts/perf/seed.sql
--   # smaller run: psql -v n_products=2000 -v n_customers=10000 -v n_orders=30000 ...
--
-- Refuses to run unless the database name contains "perf" and the catalog
-- is empty, so it can never pollute a dev/e2e/prod database.

\set ON_ERROR_STOP on

-- Scale knobs (override with psql -v n_products=... etc.). Defaults are the
-- full target dataset; variants are ~12 per product, order items ~2 per
-- order, addresses 80% of customers.
\if :{?n_products}
\else
  \set n_products 5000
\endif
\if :{?n_customers}
\else
  \set n_customers 30000
\endif
\if :{?n_orders}
\else
  \set n_orders 100000
\endif

DO $$
BEGIN
  IF current_database() NOT LIKE '%perf%' THEN
    RAISE EXCEPTION 'seed.sql only runs on a *perf* database, not %', current_database();
  END IF;
  IF EXISTS (SELECT 1 FROM products) OR EXISTS (SELECT 1 FROM orders) THEN
    RAISE EXCEPTION 'database % is not empty', current_database();
  END IF;
END $$;

-- Deterministic random(): parallel workers would each get their own seed.
SET max_parallel_workers_per_gather = 0;
SELECT setseed(0.4242);

BEGIN;

-- ---------------------------------------------------------------- points
INSERT INTO points_of_sale (id, name, address, city, working_hours, latitude, longitude, is_active, created_at)
SELECT md5('point-' || i)::uuid,
       'Cozy ' || (ARRAY['Дордой', 'ЦУМ', 'Бишкек Парк', 'Ала-Арча', 'Ош базар'])[i],
       'ул. Тестовая, ' || i, 'Бишкек', '10:00–20:00',
       42.87 + i / 100.0, 74.59 + i / 100.0, true,
       now() - interval '3 years'
FROM generate_series(1, 5) i;

-- ------------------------------------------------------------ categories
CREATE TEMP TABLE perf_top (i int, slug text, name text, kind text);
INSERT INTO perf_top VALUES
  (1, 'women', 'Женская обувь', 'women'),
  (2, 'men', 'Мужская обувь', 'men'),
  (3, 'kids', 'Детская обувь', 'kids'),
  (4, 'accessories', 'Аксессуары', 'acc');

CREATE TEMP TABLE perf_sub (j int, slug text, name text);
INSERT INTO perf_sub VALUES
  (1, 'sneakers', 'Кроссовки'), (2, 'boots', 'Ботинки'), (3, 'shoes', 'Туфли'),
  (4, 'high-boots', 'Сапоги'), (5, 'sandals', 'Сандалии'), (6, 'keds', 'Кеды'),
  (7, 'slippers', 'Тапочки');

INSERT INTO categories (id, parent_id, name_ru, name_ky, slug, sort_order)
SELECT md5('cat-' || t.i)::uuid, NULL, t.name, t.name || ' (ky)', t.slug, t.i
FROM perf_top t;

INSERT INTO categories (id, parent_id, name_ru, name_ky, slug, sort_order)
SELECT md5('cat-' || t.i || '-' || s.j)::uuid, md5('cat-' || t.i)::uuid,
       s.name, s.name || ' (ky)', t.slug || '-' || s.slug, s.j
FROM perf_top t CROSS JOIN perf_sub s;

-- -------------------------------------------------------------- products
CREATE TEMP TABLE perf_products AS
SELECT i,
       md5('product-' || i)::uuid AS id,
       top_i,
       sub_j,
       (ARRAY['Nike', 'Adidas', 'Puma', 'Reebok', 'New Balance', 'Asics', 'Skechers', 'Ecco',
              'Geox', 'Rieker', 'Tamaris', 'Caprice', 'Salamander', 'Lacoste', 'Converse', 'Vans',
              'Fila', 'Timberland', 'Dr. Martens', 'Clarks', 'Columbia', 'Merrell', 'Under Armour',
              'Kappa', 'Mizuno', 'Saucony', 'Hush Puppies', 'Camper', 'Kari', 'Zenden'])[1 + floor(30 * r_brand ^ 2)::int] AS brand,
       (ARRAY['Air', 'Classic', 'Run', 'Urban', 'Comfort', 'Pro', 'Trail', 'Street', 'Soft', 'Lite',
              'Max', 'Flex', 'Winter', 'City', 'Sport'])[1 + floor(15 * r_model)::int] AS model,
       round((1500 + 13500 * r_price ^ 2) / 10) * 10 AS base_price,
       r_active < 0.92 AS is_active,
       now() - (730 * r_created) * interval '1 day' AS created_at,
       1 + (i % 3) AS n_colors
FROM (
  SELECT i,
         -- women 40%, men 35%, kids 18%, accessories 7%
         CASE WHEN r_top < 0.40 THEN 1 WHEN r_top < 0.75 THEN 2 WHEN r_top < 0.93 THEN 3 ELSE 4 END AS top_i,
         1 + floor(7 * r_sub)::int AS sub_j,
         r_brand, r_model, r_price, r_active, r_created
  FROM (
    SELECT i, random() AS r_top, random() AS r_sub, random() AS r_brand, random() AS r_model,
           random() AS r_price, random() AS r_active, random() AS r_created
    FROM generate_series(1, :n_products) i
  ) r
) x;

INSERT INTO products (id, category_id, name_ru, name_ky, description_ru, description_ky,
                      brand, base_price, is_active, created_at, updated_at)
SELECT p.id,
       -- 5% of products sit directly on the top-level category
       CASE WHEN p.i % 20 = 0 THEN md5('cat-' || p.top_i)::uuid
            ELSE md5('cat-' || p.top_i || '-' || p.sub_j)::uuid END,
       s.name || ' ' || p.brand || ' ' || p.model || ' ' || p.i,
       s.name || ' ' || p.brand || ' ' || p.model || ' ' || p.i || ' (ky)',
       'Удобная модель ' || p.brand || ' ' || p.model || '. Натуральные материалы, анатомическая стелька.',
       'Ыңгайлуу модель ' || p.brand || ' ' || p.model || '.',
       p.brand, p.base_price, p.is_active, p.created_at,
       p.created_at + (random() * extract(epoch FROM now() - p.created_at)) * interval '1 second'
FROM perf_products p JOIN perf_sub s ON s.j = p.sub_j;

-- ------------------------------------------------------------- variants
-- n_colors (1..3) x 6 consecutive sizes -> ~12 per product, ~60k total.
CREATE TEMP TABLE perf_variants AS
SELECT row_number() OVER (ORDER BY p.i, c.k, s.k) AS idx,
       md5('variant-' || p.i || '-' || c.k || '-' || s.k)::uuid AS id,
       p.id AS product_id, p.i AS product_i,
       CASE p.top_i
         WHEN 1 THEN (35 + s.k)::text
         WHEN 2 THEN (39 + s.k)::text
         WHEN 3 THEN (26 + 2 * s.k)::text
         ELSE (ARRAY['XS', 'S', 'M', 'L', 'XL', 'XXL'])[s.k + 1]
       END AS size,
       (ARRAY['Черный', 'Белый', 'Бежевый', 'Коричневый', 'Серый', 'Синий', 'Красный',
              'Зеленый', 'Розовый', 'Бордовый'])[1 + ((p.i * 7 + c.k * 3) % 10)] AS color,
       CASE WHEN (p.i + c.k) % 10 = 0 THEN p.base_price + 500 END AS price_override,
       p.base_price,
       p.brand, p.model
FROM perf_products p
CROSS JOIN LATERAL generate_series(0, p.n_colors - 1) c(k)
CROSS JOIN generate_series(0, 5) s(k);

INSERT INTO product_variants (id, product_id, size, color, sku, price_override)
SELECT id, product_id, size, color,
       'CZ-' || lpad(product_i::text, 5, '0') || '-' || upper(left(md5(color), 3)) || '-' || size,
       price_override
FROM perf_variants;

-- ---------------------------------------------------------------- images
-- 1..4 photos per product, the 2nd one tagged with the first variant color.
INSERT INTO product_images (id, product_id, object_key, sort_order, color)
SELECT md5('image-' || p.i || '-' || k)::uuid, p.id,
       'products/' || p.id || '/' || k || '.jpg', k,
       CASE WHEN k = 1 THEN (ARRAY['Черный', 'Белый', 'Бежевый', 'Коричневый', 'Серый', 'Синий', 'Красный',
                                   'Зеленый', 'Розовый', 'Бордовый'])[1 + ((p.i * 7) % 10)] END
FROM perf_products p
CROSS JOIN LATERAL generate_series(0, p.i % 4) k;

-- ----------------------------------------------------------------- stock
-- Each variant is carried by ~60% of the points; ~25% of those rows are
-- sold out (quantity 0).
INSERT INTO stock (variant_id, point_id, quantity, updated_at)
SELECT v.id, md5('point-' || pt)::uuid,
       CASE WHEN random() < 0.25 THEN 0 ELSE 1 + floor(random() * 8)::int END,
       now() - random() * interval '60 days'
FROM perf_variants v CROSS JOIN generate_series(1, 5) pt
WHERE random() < 0.6;

-- ------------------------------------------------------------- customers
INSERT INTO customers (id, phone, name, created_at, updated_at, lang, promo_push)
SELECT md5('customer-' || i)::uuid,
       '+996' || (700000000 + i)::text,
       'Покупатель ' || i,
       now() - (760 * random()) * interval '1 day',
       now() - (30 * random()) * interval '1 day',
       CASE WHEN i % 10 = 0 THEN 'ky' ELSE 'ru' END,
       i % 3 = 0
FROM generate_series(1, :n_customers) i;

-- The first 80% of customers have one saved address.
INSERT INTO customer_addresses (id, customer_id, label, address_text, lat, lng, is_default, created_at)
SELECT md5('address-' || i)::uuid, md5('customer-' || i)::uuid, 'Дом',
       'Бишкек, мкр. ' || (1 + i % 12) || ', дом ' || (1 + i % 90) || ', кв. ' || (1 + i % 120),
       42.80 + random() / 10, 74.55 + random() / 10, true,
       now() - (700 * random()) * interval '1 day'
FROM generate_series(1, (:n_customers * 4 / 5)) i;

-- ---------------------------------------------------------------- orders
-- Order rate grows linearly over 2 years (sqrt sampling), repeat buyers
-- are skewed (customer index ~ u^1.5).
CREATE TEMP TABLE perf_orders AS
SELECT i,
       md5('order-' || i)::uuid AS id,
       cust,
       created_at,
       -- delivery orders need a saved address (first 80% of customers)
       (cust <= :n_customers * 4 / 5 AND r_delivery < 0.7) AS is_delivery,
       1 + (i % 5) AS point_i,
       CASE
         WHEN created_at > now() - interval '2 days' THEN
           CASE WHEN r_status < 0.45 THEN 'placed' WHEN r_status < 0.75 THEN 'confirmed'
                WHEN r_status < 0.92 THEN 'courier_assigned' ELSE 'cancelled' END
         WHEN created_at > now() - interval '5 days' THEN
           CASE WHEN r_status < 0.10 THEN 'confirmed' WHEN r_status < 0.25 THEN 'courier_assigned'
                WHEN r_status < 0.88 THEN 'delivered' ELSE 'cancelled' END
         ELSE CASE WHEN r_status < 0.88 THEN 'delivered' ELSE 'cancelled' END
       END::order_status AS status,
       r_online < 0.4 AS is_online,
       CASE WHEN r_items < 0.35 THEN 1 WHEN r_items < 0.70 THEN 2 WHEN r_items < 0.95 THEN 3 ELSE 4 END AS n_items
FROM (
  SELECT i,
         1 + floor(:n_customers * random() ^ 1.5)::int AS cust,
         now() - (730 * (1 - sqrt(random()))) * interval '1 day' AS created_at,
         random() AS r_delivery, random() AS r_status, random() AS r_online, random() AS r_items
  FROM generate_series(1, :n_orders) i
) r;

INSERT INTO orders (id, order_number, customer_id, address_id, point_id, status, payment_method,
                    payment_status, total_amount, delivery_fee, comment, created_at, updated_at)
SELECT o.id,
       'COZY-' || to_char(o.created_at AT TIME ZONE 'Asia/Bishkek', 'YYYYMMDD') || '-' || lpad(o.i::text, 6, '0'),
       md5('customer-' || o.cust)::uuid,
       CASE WHEN o.is_delivery THEN md5('address-' || o.cust)::uuid END,
       md5('point-' || o.point_i)::uuid,
       o.status,
       CASE WHEN o.is_online THEN 'online_card' ELSE 'cash_on_delivery' END::payment_method,
       CASE WHEN NOT o.is_online THEN NULL
            WHEN o.status = 'cancelled' THEN 'cancelled'
            WHEN o.status = 'placed' THEN 'pending'
            ELSE 'paid' END::payment_status,
       0,
       CASE WHEN o.is_delivery THEN 200 ELSE 0 END,
       CASE WHEN o.i % 7 = 0 THEN 'Позвоните за час' END,
       o.created_at,
       o.created_at + CASE WHEN o.status = 'placed' THEN interval '0' ELSE interval '1 day' END
FROM perf_orders o;

-- ----------------------------------------------------------- order items
-- Variant popularity is a power law: idx ~ 1 + N * u^2.5, shuffled across
-- products by a fixed permutation so best sellers aren't all product #1.
CREATE TEMP TABLE perf_vperm AS
SELECT row_number() OVER (ORDER BY md5('perm-' || idx)) AS rank, v.*
FROM perf_variants v;
CREATE INDEX ON perf_vperm (rank);
ANALYZE perf_vperm;

CREATE TEMP TABLE perf_items AS
SELECT o.id AS order_id, o.i AS order_i, k,
       1 + floor((SELECT count(*) FROM perf_vperm) * random() ^ 2.5)::int AS rank,
       CASE WHEN random() < 0.9 THEN 1 ELSE 2 END AS quantity
FROM perf_orders o
CROSS JOIN LATERAL generate_series(1, o.n_items) k;

INSERT INTO order_items (id, order_id, variant_id, product_name_snapshot, size_snapshot,
                         color_snapshot, quantity, price)
SELECT md5('item-' || it.order_i || '-' || it.k)::uuid, it.order_id, v.id,
       v.brand || ' ' || v.model || ' ' || v.product_i, v.size, v.color, it.quantity,
       COALESCE(v.price_override, v.base_price)
FROM perf_items it JOIN perf_vperm v ON v.rank = it.rank;

UPDATE orders o
SET total_amount = s.items_total + o.delivery_fee
FROM (SELECT order_id, SUM(quantity * price) AS items_total FROM order_items GROUP BY order_id) s
WHERE s.order_id = o.id;

-- ------------------------------------------------------ payments, history
INSERT INTO payments (id, order_id, provider, provider_tx_id, status, amount, created_at, updated_at)
SELECT md5('payment-' || o.i)::uuid, o.id, 'bakai', 'tx-' || o.i, ord.payment_status,
       ord.total_amount, o.created_at, ord.updated_at
FROM perf_orders o JOIN orders ord ON ord.id = o.id
WHERE o.is_online;

INSERT INTO order_status_history (order_id, from_status, to_status, actor_type, note, created_at)
SELECT o.id, NULL, 'placed', 'customer', NULL, o.created_at
FROM perf_orders o;

INSERT INTO order_status_history (order_id, from_status, to_status, actor_type, note, created_at)
SELECT o.id, 'placed', o.status, 'system', 'seed', o.created_at + interval '1 day'
FROM perf_orders o WHERE o.status <> 'placed';

-- ------------------------------------------------------ favorites, cart
INSERT INTO favorites (customer_id, product_id, created_at)
SELECT md5('customer-' || (1 + floor(:n_customers / 3 * random())::int))::uuid,
       md5('product-' || (1 + floor(:n_products * random() ^ 1.5)::int))::uuid,
       now() - (365 * random()) * interval '1 day'
FROM generate_series(1, :n_customers * 4 / 3)
ON CONFLICT DO NOTHING;

INSERT INTO cart_items (customer_id, variant_id, qty, created_at)
SELECT md5('customer-' || (1 + floor(:n_customers * random())::int))::uuid, v.id, 1,
       now() - (30 * random()) * interval '1 day'
FROM generate_series(1, :n_customers * 4 / 15) g
JOIN perf_variants v ON v.idx = 1 + (g * 7919) % (SELECT count(*) FROM perf_variants)
ON CONFLICT DO NOTHING;

COMMIT;

ANALYZE;
CHECKPOINT;

SELECT 'products' AS t, count(*) FROM products
UNION ALL SELECT 'product_variants', count(*) FROM product_variants
UNION ALL SELECT 'stock', count(*) FROM stock
UNION ALL SELECT 'product_images', count(*) FROM product_images
UNION ALL SELECT 'customers', count(*) FROM customers
UNION ALL SELECT 'orders', count(*) FROM orders
UNION ALL SELECT 'order_items', count(*) FROM order_items
UNION ALL SELECT 'payments', count(*) FROM payments
UNION ALL SELECT 'order_status_history', count(*) FROM order_status_history
UNION ALL SELECT 'favorites', count(*) FROM favorites
UNION ALL SELECT 'cart_items', count(*) FROM cart_items;
