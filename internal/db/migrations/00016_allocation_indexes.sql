-- Allocations are looked up by payment when applying credit, voiding a payment and listing balances.

-- +goose Up
CREATE INDEX IF NOT EXISTS payment_allocations_payment_idx ON payment_allocations (tenant_id, payment_id);
CREATE INDEX IF NOT EXISTS supplier_allocations_payment_idx ON supplier_allocations (tenant_id, payment_id);
CREATE INDEX IF NOT EXISTS supplier_payments_supplier_idx ON supplier_payments (tenant_id, supplier_id, payment_date);
CREATE INDEX IF NOT EXISTS stock_moves_wh_idx ON stock_moves (tenant_id, warehouse_id, item_id, move_date);

-- +goose Down
DROP INDEX IF EXISTS payment_allocations_payment_idx, supplier_allocations_payment_idx, supplier_payments_supplier_idx, stock_moves_wh_idx;
