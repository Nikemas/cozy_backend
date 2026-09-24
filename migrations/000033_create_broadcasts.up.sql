-- W2 fix/promo-push: promo push broadcasts ("Рассылки" in the admin panel).
-- One row per broadcast; a background worker sends it in batches to every
-- device token of customers with promo_push = true, keyset-paginating
-- device_tokens by id. cursor_token_id + the counters are updated after
-- every batch, so a restart resumes where it stopped instead of starting
-- over (at most the in-flight batch is re-sent).
CREATE TABLE broadcasts (
  id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  title_ru         TEXT NOT NULL,
  body_ru          TEXT NOT NULL,
  title_ky         TEXT NOT NULL DEFAULT '',   -- '' → the RU text is sent to ky customers
  body_ky          TEXT NOT NULL DEFAULT '',
  link             TEXT NOT NULL DEFAULT '',   -- '' | /product/<id> | /catalog?category=<id>
  link_label       TEXT NOT NULL DEFAULT '',   -- product/category name at send time, for the history list
  status           TEXT NOT NULL DEFAULT 'queued'
    CONSTRAINT broadcasts_status_chk CHECK (status IN ('queued', 'sending', 'done', 'failed')),
  -- Idempotency: one form render = one token, so a double click / resubmit
  -- of the same form can never queue a second broadcast.
  submit_token     TEXT NOT NULL CONSTRAINT broadcasts_submit_token_key UNIQUE,
  created_by       UUID REFERENCES staff(id) ON DELETE SET NULL,
  created_by_name  TEXT NOT NULL DEFAULT '',
  targets          INT NOT NULL DEFAULT 0,     -- device tokens at send start
  sent             INT NOT NULL DEFAULT 0,
  failed           INT NOT NULL DEFAULT 0,
  invalid_removed  INT NOT NULL DEFAULT 0,     -- dead tokens FCM rejected, deleted from device_tokens
  cursor_token_id  UUID,                       -- last device_tokens.id processed
  error            TEXT,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at       TIMESTAMPTZ,
  finished_at      TIMESTAMPTZ
);
CREATE INDEX idx_broadcasts_created ON broadcasts (created_at DESC);
CREATE INDEX idx_broadcasts_pending ON broadcasts (created_at) WHERE status IN ('queued', 'sending');
