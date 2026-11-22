-- feat/product-code: every product gets a code («код товара») for the
-- warehouse, support calls and reports. It reuses products.model_code (the
-- xlsx import's «артикул модели», migration 000034, unique ignoring case):
-- one column, one uniqueness rule, so a code typed in the admin form and an
-- article from the import can never collide.
--
-- Products without a code get a generated one, CZ-00001, CZ-00002, ... from
-- product_code_seq; a number already taken (an imported article that
-- happens to look like CZ-00042) is skipped. model_code_auto marks
-- generated codes: the import still treats such a product as having no
-- article of its own (it may match it by name and give it the file's
-- article, exactly as it did while model_code was NULL).
--
-- A trigger fills the code on every write that leaves it empty, so code
-- that doesn't know about codes yet (the previous release during a deploy,
-- the JSON admin API) keeps working against the NOT NULL column:
--   * INSERT without a code (NULL/blank)  -> generated, model_code_auto
--   * UPDATE setting it NULL/blank        -> the stored code is kept
--   * UPDATE to a different code          -> it is a real code now

SET lock_timeout = '10s';

ALTER TABLE products ADD COLUMN model_code_auto BOOLEAN NOT NULL DEFAULT false;

CREATE SEQUENCE product_code_seq;

-- next_product_code returns the next free CZ-NNNNN code (at least five
-- digits; CZ-100000 after CZ-99999).
CREATE FUNCTION next_product_code() RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
  n bigint;
  candidate text;
BEGIN
  LOOP
    n := nextval('product_code_seq');
    candidate := 'CZ-' || CASE WHEN n < 100000 THEN lpad(n::text, 5, '0') ELSE n::text END;
    IF NOT EXISTS (SELECT 1 FROM products WHERE lower(model_code) = lower(candidate)) THEN
      RETURN candidate;
    END IF;
  END LOOP;
END $$;

-- Backfill in creation order, so older products get smaller numbers.
DO $$
DECLARE
  r record;
BEGIN
  FOR r IN SELECT id FROM products
           WHERE model_code IS NULL OR btrim(model_code) = ''
           ORDER BY created_at, id
  LOOP
    UPDATE products SET model_code = next_product_code(), model_code_auto = true WHERE id = r.id;
  END LOOP;
END $$;

CREATE FUNCTION products_fill_model_code() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.model_code := NULLIF(btrim(NEW.model_code), '');
  IF TG_OP = 'UPDATE' AND NEW.model_code IS NULL THEN
    NEW.model_code := OLD.model_code;
    NEW.model_code_auto := OLD.model_code_auto;
  ELSIF TG_OP = 'UPDATE' AND lower(NEW.model_code) = lower(OLD.model_code) THEN
    NEW.model_code_auto := OLD.model_code_auto;
  ELSE
    NEW.model_code_auto := false;
  END IF;
  IF NEW.model_code IS NULL THEN
    NEW.model_code := next_product_code();
    NEW.model_code_auto := true;
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER products_fill_model_code
  BEFORE INSERT OR UPDATE ON products
  FOR EACH ROW EXECUTE FUNCTION products_fill_model_code();

ALTER TABLE products ALTER COLUMN model_code SET NOT NULL;
