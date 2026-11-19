-- Module T, part 4: professional stock.
--   * units of measure per item (buy a barrel of 200 L, sell litres): factor = base units in one unit
--   * costing per item: weighted average (default) or FIFO, through cost layers; stock.value stays the
--     sum of what is held, so the valuation still equals the inventory accounts
--   * lots / serial numbers with expiry dates, and which lot every move took (traceability)
--   * reorder rules (min / max per warehouse) for replenishment suggestions
--   * stock counts (count sheet → differences posted as an adjustment)
--   * landed costs (freight, customs) added to the value of a supplier bill's goods
--   * price lists (per currency, per customer, quantity breaks) and kits (one item made of others)

-- +goose Up
ALTER TABLE items ADD COLUMN costing text NOT NULL DEFAULT 'avg' CHECK (costing IN ('avg', 'fifo'));
ALTER TABLE items ADD COLUMN tracking text NOT NULL DEFAULT 'none' CHECK (tracking IN ('none', 'lot', 'serial'));
ALTER TABLE items DROP CONSTRAINT IF EXISTS items_kind_check;
ALTER TABLE items ADD CONSTRAINT items_kind_check CHECK (kind IN ('stock', 'service', 'consumable', 'kit'));

ALTER TABLE stock_moves DROP CONSTRAINT IF EXISTS stock_moves_source_check;
ALTER TABLE stock_moves ADD CONSTRAINT stock_moves_source_check CHECK (source IN ('bill', 'bill_void', 'fuel', 'adjust', 'sale', 'sale_void',
  'stock_void', 'fuel_void', 'adjust_void', 'transfer', 'transfer_void', 'credit_note', 'credit_note_void', 'debit_note', 'debit_note_void',
  'delivery', 'delivery_void', 'pos', 'pos_void', 'pos_refund', 'pos_refund_void', 'count', 'landed_cost'));

-- ---- units of measure ----
CREATE TABLE item_units (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  item_id    uuid NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) > 0),
  factor     numeric(18, 6) NOT NULL CHECK (factor > 0),
  barcode    text,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT item_units_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (item_id, name)
);

-- ---- FIFO cost layers ----
CREATE TABLE stock_layers (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  item_id      uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  layer_date   date NOT NULL,
  source       text NOT NULL,
  source_id    uuid,
  qty_left     numeric(18, 4) NOT NULL CHECK (qty_left >= 0),
  value_left   numeric(20, 4) NOT NULL CHECK (value_left >= 0),
  unit_cost    numeric(20, 6) NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT stock_layers_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  CONSTRAINT stock_layers_warehouse_id_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id)
);
CREATE INDEX stock_layers_open_idx ON stock_layers (item_id, warehouse_id, layer_date, created_at) WHERE qty_left > 0;

-- ---- lots and serial numbers ----
CREATE TABLE stock_lots (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  item_id    uuid NOT NULL,
  lot_no     text NOT NULL CHECK (length(trim(lot_no)) > 0),
  expiry     date,
  notes      text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT stock_lots_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, item_id, lot_no)
);
CREATE TABLE stock_lot_qty (
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  lot_id       uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  quantity     numeric(18, 4) NOT NULL DEFAULT 0 CHECK (quantity >= 0),
  PRIMARY KEY (lot_id, warehouse_id),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT stock_lot_qty_lot_id_same_company FOREIGN KEY (company_id, lot_id) REFERENCES stock_lots (company_id, id),
  CONSTRAINT stock_lot_qty_warehouse_id_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id)
);
CREATE TABLE stock_move_lots (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  move_id    uuid NOT NULL REFERENCES stock_moves (id),
  lot_id     uuid NOT NULL,
  quantity   numeric(18, 4) NOT NULL CHECK (quantity <> 0),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT stock_move_lots_lot_id_same_company FOREIGN KEY (company_id, lot_id) REFERENCES stock_lots (company_id, id)
);
CREATE INDEX stock_move_lots_move_idx ON stock_move_lots (move_id);
CREATE INDEX stock_move_lots_lot_idx ON stock_move_lots (lot_id);

-- ---- reorder rules ----
CREATE TABLE reorder_rules (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  item_id      uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  min_qty      numeric(18, 4) NOT NULL CHECK (min_qty >= 0),
  max_qty      numeric(18, 4) NOT NULL,
  supplier_id  uuid,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT reorder_rules_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  CONSTRAINT reorder_rules_warehouse_id_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id),
  CONSTRAINT reorder_rules_supplier_id_same_company FOREIGN KEY (company_id, supplier_id) REFERENCES suppliers (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (item_id, warehouse_id),
  CHECK (max_qty >= min_qty)
);

-- ---- stock counts ----
CREATE TABLE stock_counts (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  warehouse_id     uuid NOT NULL,
  count_date       date NOT NULL,
  status           text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'posted', 'cancelled')),
  journal_entry_id uuid,
  notes            text NOT NULL DEFAULT '',
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  posted_at        timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT stock_counts_warehouse_id_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id),
  CONSTRAINT stock_counts_journal_entry_id_same_company FOREIGN KEY (company_id, journal_entry_id) REFERENCES journal_entries (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
CREATE TABLE stock_count_lines (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  count_id   uuid NOT NULL,
  item_id    uuid NOT NULL,
  expected   numeric(18, 4) NOT NULL,
  counted    numeric(18, 4) CHECK (counted >= 0),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT stock_count_lines_count_id_same_company FOREIGN KEY (company_id, count_id) REFERENCES stock_counts (company_id, id),
  CONSTRAINT stock_count_lines_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  UNIQUE (count_id, item_id)
);

-- ---- landed costs ----
CREATE TABLE landed_costs (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  bill_id          uuid NOT NULL,
  cost_date        date NOT NULL,
  currency         char(3) NOT NULL REFERENCES currencies (code),
  amount           numeric(20, 4) NOT NULL CHECK (amount > 0),
  amount_base      numeric(20, 4) NOT NULL,
  method           text NOT NULL DEFAULT 'value' CHECK (method IN ('value', 'quantity')),
  description      text NOT NULL DEFAULT '',
  journal_entry_id uuid,
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT landed_costs_bill_id_same_company FOREIGN KEY (company_id, bill_id) REFERENCES bills (company_id, id),
  CONSTRAINT landed_costs_journal_entry_id_same_company FOREIGN KEY (company_id, journal_entry_id) REFERENCES journal_entries (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);

-- ---- price lists ----
CREATE TABLE price_lists (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) > 0),
  currency   char(3) NOT NULL REFERENCES currencies (code),
  is_active  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, name)
);
CREATE TABLE price_list_items (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  list_id    uuid NOT NULL,
  item_id    uuid NOT NULL,
  min_qty    numeric(18, 4) NOT NULL DEFAULT 0 CHECK (min_qty >= 0),
  price      numeric(20, 4) NOT NULL CHECK (price >= 0),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT price_list_items_list_id_same_company FOREIGN KEY (company_id, list_id) REFERENCES price_lists (company_id, id),
  CONSTRAINT price_list_items_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  UNIQUE (list_id, item_id, min_qty)
);
ALTER TABLE customers ADD COLUMN price_list_id uuid;
ALTER TABLE customers ADD CONSTRAINT customers_price_list_id_same_company FOREIGN KEY (company_id, price_list_id) REFERENCES price_lists (company_id, id);

-- ---- kits ----
CREATE TABLE item_components (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  kit_id       uuid NOT NULL,
  component_id uuid NOT NULL,
  quantity     numeric(18, 4) NOT NULL CHECK (quantity > 0),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT item_components_kit_id_same_company FOREIGN KEY (company_id, kit_id) REFERENCES items (company_id, id),
  CONSTRAINT item_components_component_id_same_company FOREIGN KEY (company_id, component_id) REFERENCES items (company_id, id),
  UNIQUE (kit_id, component_id),
  CHECK (kit_id <> component_id)
);

-- row level security: tenant isolation + company scope (see 00021)
-- +goose StatementBegin
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['item_units', 'stock_layers', 'stock_lots', 'stock_lot_qty', 'stock_move_lots', 'reorder_rules',
                           'stock_counts', 'stock_count_lines', 'landed_costs', 'price_lists', 'price_list_items', 'item_components'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
  END LOOP;
END
$$;
-- +goose StatementEnd
-- rows that describe configuration (not money) may be removed
GRANT DELETE ON item_units, reorder_rules, price_list_items, item_components, stock_count_lines TO paxve_app;

-- (cost layers exist only for FIFO items: switching an item to FIFO opens one layer per warehouse from its stock)

-- +goose Down
ALTER TABLE customers DROP COLUMN IF EXISTS price_list_id;
DROP TABLE IF EXISTS item_components, price_list_items, price_lists, landed_costs, stock_count_lines, stock_counts,
  reorder_rules, stock_move_lots, stock_lot_qty, stock_lots, stock_layers, item_units;
ALTER TABLE items DROP COLUMN IF EXISTS tracking;
ALTER TABLE items DROP COLUMN IF EXISTS costing;
