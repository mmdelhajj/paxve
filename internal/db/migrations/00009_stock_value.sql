-- Track the exact stock value (base currency) per item/warehouse, so the stock valuation always equals
-- the inventory accounts in the ledger (no drift from a rounded average cost).

-- +goose Up
ALTER TABLE stock ADD COLUMN value numeric(20, 4) NOT NULL DEFAULT 0;
UPDATE stock s SET value = coalesce((SELECT sum(m.value) FROM stock_moves m
  WHERE m.item_id = s.item_id AND m.warehouse_id = s.warehouse_id AND m.tenant_id = s.tenant_id), 0);
ALTER TABLE stock ADD CONSTRAINT stock_value_check CHECK (value >= 0 AND (quantity > 0 OR value = 0));

-- +goose Down
ALTER TABLE stock DROP COLUMN IF EXISTS value;
