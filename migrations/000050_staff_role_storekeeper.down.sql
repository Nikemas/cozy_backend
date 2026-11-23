-- Postgres can't drop an enum value, so the type is rebuilt without it.
-- Storekeepers become point_staff accounts that are DEACTIVATED (and
-- logged out): after a rollback the older code would otherwise give them
-- the orders screen of their warehouse, which they never had. The owner
-- can re-activate or re-assign them by hand.
WITH demoted AS (
  UPDATE staff SET role = 'point_staff', is_active = false
  WHERE role = 'storekeeper'
  RETURNING id
)
DELETE FROM staff_sessions WHERE staff_id IN (SELECT id FROM demoted);

ALTER TYPE staff_role RENAME TO staff_role_old;
CREATE TYPE staff_role AS ENUM ('owner', 'manager', 'point_staff');
ALTER TABLE staff ALTER COLUMN role TYPE staff_role USING role::text::staff_role;
DROP TYPE staff_role_old;
