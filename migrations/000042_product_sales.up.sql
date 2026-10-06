-- perf/db-at-scale: units sold per product for the catalog's sort=popular.
--
-- catalog.ProductRepo.List used to compute popularity on every request as
--   SUM(order_items.quantity) over orders with status <> 'cancelled',
--   grouped by product
-- i.e. a full aggregation of order_items JOIN orders JOIN product_variants
-- per page view (p95 > 2 s at 20 concurrent requests on the perf dataset,
-- see docs/performance.md). product_sales keeps exactly that number,
-- maintained by triggers in the same transaction as the write that changes
-- it, so the sort stays exact (no staleness window) and becomes a PK join.
--
-- What changes the number, and the trigger that handles it:
--   * order_items INSERT / DELETE / UPDATE (quantity, variant_id, order_id)
--       -> order_items_product_sales (row level)
--   * orders.status crossing 'cancelled' in either direction
--       -> orders_product_sales_status
--   * orders DELETE: subtracts the order's items BEFORE the row goes, the
--       cascaded order_items deletes then find no order and skip
--       -> orders_product_sales_delete
--   * product_variants.product_id changing moves the variant's units
--       -> product_variants_product_sales
-- Multi-product updates run in product_id order so two transactions never
-- take product_sales row locks in opposite orders (no deadlocks); checkout
-- inserts its items in product order for the same reason
-- (orders.Service.CreateOrder).

CREATE TABLE product_sales (
  product_id UUID PRIMARY KEY REFERENCES products(id) ON DELETE CASCADE,
  units_sold BIGINT NOT NULL DEFAULT 0
);

-- Adds p_delta units to one product's counter (creating its row).
CREATE FUNCTION product_sales_add(p_product UUID, p_delta BIGINT) RETURNS void
LANGUAGE sql AS $$
  INSERT INTO product_sales (product_id, units_sold) VALUES (p_product, p_delta)
  ON CONFLICT (product_id) DO UPDATE SET units_sold = product_sales.units_sold + EXCLUDED.units_sold;
$$;

-- Adds (p_sign = 1) or removes (-1) an order's items, per product, in
-- product_id order.
CREATE FUNCTION product_sales_apply_order(p_order UUID, p_sign INT) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
  r RECORD;
BEGIN
  FOR r IN
    SELECT pv.product_id, SUM(oi.quantity) AS units
    FROM order_items oi JOIN product_variants pv ON pv.id = oi.variant_id
    WHERE oi.order_id = p_order
    GROUP BY pv.product_id
    ORDER BY pv.product_id
  LOOP
    PERFORM product_sales_add(r.product_id, p_sign * r.units);
  END LOOP;
END $$;

-- Whether an order_items row of p_order counts: its order exists (during an
-- order's cascaded delete it no longer does — orders_product_sales_delete
-- already took the units off) and isn't cancelled.
CREATE FUNCTION product_sales_item_counts(p_order UUID) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT EXISTS (SELECT 1 FROM orders WHERE id = p_order AND status <> 'cancelled');
$$;

CREATE FUNCTION order_items_product_sales() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP IN ('DELETE', 'UPDATE') AND product_sales_item_counts(OLD.order_id) THEN
    PERFORM product_sales_add((SELECT product_id FROM product_variants WHERE id = OLD.variant_id), -OLD.quantity);
  END IF;
  IF TG_OP IN ('INSERT', 'UPDATE') AND product_sales_item_counts(NEW.order_id) THEN
    PERFORM product_sales_add((SELECT product_id FROM product_variants WHERE id = NEW.variant_id), NEW.quantity);
  END IF;
  RETURN NULL;
END $$;

CREATE TRIGGER order_items_product_sales
AFTER INSERT OR DELETE OR UPDATE OF quantity, variant_id, order_id ON order_items
FOR EACH ROW EXECUTE FUNCTION order_items_product_sales();

CREATE FUNCTION orders_product_sales_status() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status <> 'cancelled' AND NEW.status = 'cancelled' THEN
    PERFORM product_sales_apply_order(NEW.id, -1);
  ELSIF OLD.status = 'cancelled' AND NEW.status <> 'cancelled' THEN
    PERFORM product_sales_apply_order(NEW.id, 1);
  END IF;
  RETURN NULL;
END $$;

CREATE TRIGGER orders_product_sales_status
AFTER UPDATE OF status ON orders
FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status)
EXECUTE FUNCTION orders_product_sales_status();

CREATE FUNCTION orders_product_sales_delete() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status <> 'cancelled' THEN
    PERFORM product_sales_apply_order(OLD.id, -1);
  END IF;
  RETURN OLD;
END $$;

CREATE TRIGGER orders_product_sales_delete
BEFORE DELETE ON orders
FOR EACH ROW EXECUTE FUNCTION orders_product_sales_delete();

CREATE FUNCTION product_variants_product_sales() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  moved BIGINT;
BEGIN
  SELECT COALESCE(SUM(oi.quantity), 0) INTO moved
  FROM order_items oi JOIN orders o ON o.id = oi.order_id
  WHERE oi.variant_id = NEW.id AND o.status <> 'cancelled';
  IF moved <> 0 THEN
    IF OLD.product_id < NEW.product_id THEN
      PERFORM product_sales_add(OLD.product_id, -moved);
      PERFORM product_sales_add(NEW.product_id, moved);
    ELSE
      PERFORM product_sales_add(NEW.product_id, moved);
      PERFORM product_sales_add(OLD.product_id, -moved);
    END IF;
  END IF;
  RETURN NULL;
END $$;

CREATE TRIGGER product_variants_product_sales
AFTER UPDATE OF product_id ON product_variants
FOR EACH ROW WHEN (OLD.product_id IS DISTINCT FROM NEW.product_id)
EXECUTE FUNCTION product_variants_product_sales();

-- Backfill from existing orders.
INSERT INTO product_sales (product_id, units_sold)
SELECT pv.product_id, SUM(oi.quantity)
FROM order_items oi
JOIN product_variants pv ON pv.id = oi.variant_id
JOIN orders o ON o.id = oi.order_id
WHERE o.status <> 'cancelled'
GROUP BY pv.product_id;
