-- App Store guideline 4.5.4: marketing pushes must be opt-in. New customers
-- start with promo_push = false and the app asks for consent; the answer
-- (PUT /api/v1/customer {"promo_push": ...}) stamps promo_push_asked_at so
-- the app knows not to ask again (exposed as promo_push_asked).
--
-- Existing customers' promo_push values are deliberately NOT changed here:
-- they can already switch promos off in Profile → "Акции и скидки", and
-- whether to reset everyone to false (re-asking them) is a business
-- decision for the shop owner, to be done as a separate data change if
-- wanted. Their promo_push_asked_at stays NULL, so the app will show them
-- the consent prompt once as well.
ALTER TABLE customers
  ALTER COLUMN promo_push SET DEFAULT false,
  ADD COLUMN promo_push_asked_at TIMESTAMPTZ;
