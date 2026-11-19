-- Contact number for the courier (the recipient's phone), entered at
-- checkout for delivery orders. It may differ from the account phone (a
-- gift, a relative receives the parcel). Stored in the canonical
-- +996XXXXXXXXX form (internal/phonenorm), validated by
-- orders.normalizeCourierPhone.
--
-- Nullable: pickup orders have none, older orders predate it, and an older
-- app build may not send it — staff then call the account phone. No
-- backfill: copying the account phone would claim the customer confirmed it.

-- ADD COLUMN takes ACCESS EXCLUSIVE on orders; fail fast instead of
-- queueing behind an in-flight checkout during a deploy.
SET lock_timeout = '10s';

ALTER TABLE orders
  ADD COLUMN IF NOT EXISTS courier_phone TEXT;
