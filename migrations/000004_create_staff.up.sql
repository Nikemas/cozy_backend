CREATE TYPE staff_role AS ENUM ('owner', 'manager', 'point_staff');

CREATE TABLE staff (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  phone         TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,            -- bcrypt
  name          TEXT NOT NULL,
  role          staff_role NOT NULL,
  point_id      UUID REFERENCES points_of_sale(id), -- NULL для owner/manager
  is_active     BOOLEAN NOT NULL DEFAULT true,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
