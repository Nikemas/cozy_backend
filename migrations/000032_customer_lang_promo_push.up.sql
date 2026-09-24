-- W2 fix/promo-push: per-customer language (order-status pushes/SMS and
-- promo broadcasts are rendered in it; the storefront's language switch and
-- PUT /api/v1/customer set it) and the "Акции и скидки" opt-out for promo
-- pushes. Order-status pushes are transactional and ignore promo_push.
ALTER TABLE customers
  ADD COLUMN lang TEXT NOT NULL DEFAULT 'ru'
    CONSTRAINT customers_lang_chk CHECK (lang IN ('ru', 'ky')),
  ADD COLUMN promo_push BOOLEAN NOT NULL DEFAULT true;
