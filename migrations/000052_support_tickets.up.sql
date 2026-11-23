-- feat/support-tickets: customer support requests ("Поддержка") from the
-- site and the app, handled by staff in the admin panel (Обращения).
--
-- support_tickets is the request itself; the customer's text and every
-- later reply live in support_ticket_messages (the first message is the
-- request text), so staff can answer and the customer can follow up.
--
-- New tables only: older code never touches them (expand-only).

CREATE TABLE support_tickets (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  -- Short human-readable number ("Обращение №123") for staff and customers.
  number      BIGINT GENERATED ALWAYS AS IDENTITY UNIQUE,
  -- NULL for a guest request from the site. Deleting the account deletes
  -- its requests (auth.deleteCustomer); the FK only guards consistency.
  customer_id UUID REFERENCES customers(id) ON DELETE CASCADE,
  name        TEXT NOT NULL DEFAULT '',
  phone       TEXT NOT NULL,                  -- +996XXXXXXXXX (internal/phonenorm)
  topic       TEXT NOT NULL
    CONSTRAINT support_tickets_topic_chk CHECK (topic IN ('order', 'payment', 'delivery', 'return', 'product', 'other')),
  -- Only an order of the same customer (checked by internal/support).
  order_id    UUID REFERENCES orders(id) ON DELETE SET NULL,
  channel     TEXT NOT NULL
    CONSTRAINT support_tickets_channel_chk CHECK (channel IN ('web', 'app')),
  status      TEXT NOT NULL DEFAULT 'new'
    CONSTRAINT support_tickets_status_chk CHECK (status IN ('new', 'in_progress', 'closed')),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  closed_at   TIMESTAMPTZ,
  CONSTRAINT support_tickets_closed_at_chk CHECK ((status = 'closed') = (closed_at IS NOT NULL))
);

-- Admin list: newest first, optionally by status; the sidebar badge
-- counts status = 'new'.
CREATE INDEX idx_support_tickets_created ON support_tickets (created_at DESC);
CREATE INDEX idx_support_tickets_status_created ON support_tickets (status, created_at DESC);
-- A customer's own requests (site + app), newest activity first.
CREATE INDEX idx_support_tickets_customer ON support_tickets (customer_id, updated_at DESC)
  WHERE customer_id IS NOT NULL;
-- Per-phone rate limit on creation (guests have no customer_id).
CREATE INDEX idx_support_tickets_phone_created ON support_tickets (phone, created_at DESC);

CREATE TABLE support_ticket_messages (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id  UUID NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
  author     TEXT NOT NULL
    CONSTRAINT support_ticket_messages_author_chk CHECK (author IN ('customer', 'staff')),
  staff_id   UUID REFERENCES staff(id) ON DELETE SET NULL,
  body       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT support_ticket_messages_staff_chk CHECK (author = 'staff' OR staff_id IS NULL)
);
CREATE INDEX idx_support_ticket_messages_ticket ON support_ticket_messages (ticket_id, created_at);
