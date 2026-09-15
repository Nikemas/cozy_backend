CREATE TABLE categories (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  parent_id     UUID REFERENCES categories(id),
  name_ru       TEXT NOT NULL,
  name_ky       TEXT NOT NULL,
  slug          TEXT NOT NULL UNIQUE,
  sort_order    INT NOT NULL DEFAULT 0
);
