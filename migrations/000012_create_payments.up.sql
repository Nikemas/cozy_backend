CREATE TYPE payment_status AS ENUM ('pending', 'paid', 'failed', 'refunded');

CREATE TABLE payments (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id      UUID NOT NULL REFERENCES orders(id),
  provider      TEXT NOT NULL DEFAULT 'bakai',
  provider_tx_id TEXT,
  status        payment_status NOT NULL DEFAULT 'pending',
  amount        NUMERIC(10,2) NOT NULL,
  raw_webhook   JSONB,                    -- сырой вебхук для отладки
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
