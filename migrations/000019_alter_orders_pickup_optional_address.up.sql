-- Task 3 (Cart + Checkout + Done): 000010_create_orders made address_id
-- NOT NULL, which only allows delivery orders. The web-plan's checkout
-- explicitly supports "delivery OR self-pickup" (see
-- orders.Service.CreateOrder), so a self-pickup order has no delivery
-- address at all. Relax the column and add a CHECK requiring at least one
-- of address_id/point_id to identify how/where the order is handled —
-- note this isn't "exactly one": a delivery order also records point_id
-- as the warehouse it was picked/packed from (chosen automatically by
-- stock availability), while a pickup order's point_id is the customer's
-- chosen pickup location and it has no address_id at all. The "customer
-- picks exactly one of delivery-address/pickup-point" rule that the
-- web-plan describes is enforced in orders.Service.CreateOrder against
-- its own two input parameters, not as a single DB column pairing.
ALTER TABLE orders ALTER COLUMN address_id DROP NOT NULL;
ALTER TABLE orders ADD CONSTRAINT orders_address_or_point_chk
  CHECK (address_id IS NOT NULL OR point_id IS NOT NULL);
