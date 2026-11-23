-- Marks product photos that still carry the white 8% frame the old upload
-- code baked in (see internal/media/reprocess.go). Every photo that exists
-- when this migration runs was uploaded by the old code, so it is marked;
-- photos uploaded afterwards (the new code saves them edge to edge) default
-- to false. `server reprocess-photos --apply` reframes the marked photos and
-- clears the mark, which makes the command safe to run again and keeps it
-- away from new photos with a white studio background.
--
-- Expand-only: old code ignores the column.
SET lock_timeout = '10s';

ALTER TABLE product_images ADD COLUMN legacy_frame BOOLEAN NOT NULL DEFAULT false;
UPDATE product_images SET legacy_frame = true;
