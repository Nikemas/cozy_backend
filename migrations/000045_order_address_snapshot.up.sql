-- Delivery address snapshot on the order (mobile order-detail screen,
-- admin order page). orders.address_id points at customer_addresses, which
-- the customer can edit at any time (PUT /api/v1/addresses/{id}, web
-- /addresses), so reading the address live rewrote order history.
-- orders.Service.createOrder now copies the address's label/text into
-- these columns in the order's own transaction, like order_items already
-- snapshots name/size/color/price.
--
-- Both nullable: pickup orders have no address, and the label is optional
-- on customer_addresses too. address_id stays (FK, "which saved address").

-- ADD COLUMN takes ACCESS EXCLUSIVE on orders and the backfill row-locks
-- every delivery order; don't queue behind an in-flight checkout during a
-- deploy — fail fast and let the deploy retry.
SET lock_timeout = '10s';

ALTER TABLE orders
  ADD COLUMN IF NOT EXISTS delivery_address_text  TEXT,
  ADD COLUMN IF NOT EXISTS delivery_address_label TEXT;

-- Backfill existing delivery orders from the address as it is now (the best
-- we have: earlier edits are already lost). Blank labels become NULL, as in
-- createOrder. Rows whose snapshot is already set are left alone so a
-- re-run is harmless. No trigger fires: orders' only UPDATE trigger
-- (000042) is UPDATE OF status, and updated_at is not touched.
UPDATE orders o
SET delivery_address_text  = a.address_text,
    delivery_address_label = NULLIF(btrim(a.label), '')
FROM customer_addresses a
WHERE a.id = o.address_id
  AND o.delivery_address_text IS NULL;
