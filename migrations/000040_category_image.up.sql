-- feat/category-photo: an optional photo per category, uploaded from the
-- admin panel (/admin/categories) and shown on the mobile app's catalog
-- category tiles (GET /api/v1/categories → image_url).
--
-- image_key is a MinIO object key (categories/<uuid>.<ext>), stored the
-- same way as home_banner.image_key (000039): the public URL is built at
-- read time from MINIO_PUBLIC_ENDPOINT (config.PublicObjectURL). NULL =
-- no photo.
ALTER TABLE categories ADD COLUMN image_key TEXT;
