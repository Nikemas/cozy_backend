-- feat/home-banner: the promo banner at the top of the site's home page and
-- the mobile app's home screen, edited from the admin panel
-- (/admin/banner) and served to the app by GET /api/v1/banner.
--
-- A singleton: exactly one row (id = 1). Texts are per language; an empty
-- Kyrgyz text falls back to the Russian one at read time. Colors are
-- validated "#RRGGBB" so they can go into an inline style safely.
-- image_key / bg_image_key are MinIO object keys (banners/<uuid>.<ext>).
CREATE TABLE home_banner (
  id               SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  enabled          BOOLEAN NOT NULL DEFAULT true,
  eyebrow_ru       TEXT NOT NULL DEFAULT '' CHECK (char_length(eyebrow_ru) <= 60),
  eyebrow_ky       TEXT NOT NULL DEFAULT '' CHECK (char_length(eyebrow_ky) <= 60),
  title_ru         TEXT NOT NULL DEFAULT '' CHECK (char_length(title_ru) <= 120),
  title_ky         TEXT NOT NULL DEFAULT '' CHECK (char_length(title_ky) <= 120),
  button_ru        TEXT NOT NULL DEFAULT '' CHECK (char_length(button_ru) <= 30),
  button_ky        TEXT NOT NULL DEFAULT '' CHECK (char_length(button_ky) <= 30),
  link_category_id UUID REFERENCES categories(id) ON DELETE SET NULL,
  bg_color         TEXT NOT NULL DEFAULT '#FFF3E9' CHECK (bg_color ~ '^#[0-9A-Fa-f]{6}$'),
  text_color       TEXT NOT NULL DEFAULT '#1A1A1A' CHECK (text_color ~ '^#[0-9A-Fa-f]{6}$'),
  eyebrow_color    TEXT NOT NULL DEFAULT '#B35400' CHECK (eyebrow_color ~ '^#[0-9A-Fa-f]{6}$'),
  image_key        TEXT,
  bg_image_key     TEXT,
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Seeded with the texts the home page showed before the banner became
-- editable (locales/*.yaml shop.banner.*), so nothing changes visually
-- until the owner edits it. Colors are the site's --cozy-accent-bg,
-- --cozy-ink and --cozy-accent-ink (the eyebrow) tokens
-- (web/static/css/site.css).
INSERT INTO home_banner (id, enabled, eyebrow_ru, eyebrow_ky, title_ru, title_ky, button_ru, button_ky)
VALUES (
  1, true,
  'Обувь для всей семьи',
  'Бүт үй-бүлө үчүн бут кийим',
  'Доставка по Бишкеку и самовывоз из наших магазинов',
  'Бишкек боюнча жеткирүү жана дүкөндөрүбүздөн алып кетүү',
  'Смотреть',
  'Көрүү'
);
