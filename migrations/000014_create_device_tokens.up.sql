CREATE TABLE device_tokens (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  customer_id   UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
  fcm_token     TEXT NOT NULL UNIQUE,
  platform      TEXT NOT NULL,            -- ios|android
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
