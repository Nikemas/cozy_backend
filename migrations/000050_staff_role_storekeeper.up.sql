-- Кладовщик: a staff member pinned to one warehouse (staff.point_id,
-- kind = 'warehouse'), who sees only Остатки and Склады of it.
-- ADD VALUE may run inside the migration's implicit transaction (PG12+)
-- as long as the new value isn't used in that same transaction — so
-- nothing else goes into this file.
ALTER TYPE staff_role ADD VALUE IF NOT EXISTS 'storekeeper';
