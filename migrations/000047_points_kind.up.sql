-- Points of sale get a kind: a 'branch' (филиал) stores goods, is shown on
-- the map / branches screen and can be a self-pickup point; a 'warehouse'
-- (склад) only stores goods — its stock counts toward availability and
-- delivery fulfilment, but customers never see it or pick it for pickup.
-- Every existing point stays a branch.
ALTER TABLE points_of_sale
  ADD COLUMN kind TEXT NOT NULL DEFAULT 'branch',
  ADD CONSTRAINT points_of_sale_kind_chk CHECK (kind IN ('branch', 'warehouse'));
