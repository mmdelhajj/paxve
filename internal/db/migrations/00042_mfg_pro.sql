-- Phase 2, manufacturing pro: BoM versions (one active per family), phantom (kit) and subcontracting BoMs,
-- by-products, work centers and routings, manufacturing orders that are confirmed (components reserved, work
-- orders created from the routing), produced in parts (backorders), scrapped, traced by lot; MRP from sales
-- demand and reorder rules; quality control points, checks and alerts; shop-floor timers; standard cost with the
-- production variance posted to the ledger.
--
-- Money: components leave stock at their cost, labour / machine time is charged at the work center's cost per
-- hour (credited to the production-absorbed account), subcontracting service is credited to a "received not
-- billed" account that the subcontractor's bill clears. When the finished item has a standard cost it enters
-- stock at that cost and the difference goes to the production variance account.

-- +goose Up
ALTER TABLE items ADD COLUMN standard_cost numeric(20, 6) NOT NULL DEFAULT 0 CHECK (standard_cost >= 0);

ALTER TABLE companies ADD COLUMN mfg_variance_account_id uuid, ADD COLUMN mfg_subcontract_account_id uuid;
ALTER TABLE companies ADD CONSTRAINT companies_mfg_variance_fk FOREIGN KEY (tenant_id, mfg_variance_account_id) REFERENCES accounts (tenant_id, id);
ALTER TABLE companies ADD CONSTRAINT companies_mfg_subcontract_fk FOREIGN KEY (tenant_id, mfg_subcontract_account_id) REFERENCES accounts (tenant_id, id);

-- ---- work centers and routings ----
CREATE TABLE mfg_workcenters (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  code          text NOT NULL CHECK (length(trim(code)) BETWEEN 1 AND 30),
  name          text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
  capacity      numeric(10, 2) NOT NULL DEFAULT 1 CHECK (capacity > 0),
  hours_per_day numeric(5, 2) NOT NULL DEFAULT 8 CHECK (hours_per_day > 0 AND hours_per_day <= 24),
  days_per_week smallint NOT NULL DEFAULT 6 CHECK (days_per_week BETWEEN 1 AND 7),
  efficiency    numeric(6, 2) NOT NULL DEFAULT 100 CHECK (efficiency > 0 AND efficiency <= 500),
  cost_per_hour numeric(20, 4) NOT NULL DEFAULT 0 CHECK (cost_per_hour >= 0),
  setup_min     numeric(10, 2) NOT NULL DEFAULT 0 CHECK (setup_min >= 0),
  notes         text NOT NULL DEFAULT '',
  is_active     boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, code)
);

CREATE TABLE mfg_routings (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
  notes      text NOT NULL DEFAULT '',
  is_active  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id)
);

-- minutes: per BoM batch (the BoM's output quantity), at 100 % efficiency
CREATE TABLE mfg_routing_ops (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  routing_id    uuid NOT NULL,
  seq           int NOT NULL,
  name          text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
  workcenter_id uuid NOT NULL,
  minutes       numeric(12, 2) NOT NULL DEFAULT 0 CHECK (minutes >= 0),
  instructions  text NOT NULL DEFAULT '',
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT mfg_routing_ops_routing_id_same_company FOREIGN KEY (company_id, routing_id) REFERENCES mfg_routings (company_id, id),
  CONSTRAINT mfg_routing_ops_workcenter_id_same_company FOREIGN KEY (company_id, workcenter_id) REFERENCES mfg_workcenters (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (routing_id, seq)
);

-- ---- bills of materials: versions, types, routing, subcontracting, by-products ----
ALTER TABLE boms ADD COLUMN code text NOT NULL DEFAULT '';
ALTER TABLE boms ADD COLUMN version int NOT NULL DEFAULT 1 CHECK (version > 0);
ALTER TABLE boms ADD COLUMN family_id uuid;
ALTER TABLE boms ADD COLUMN bom_type text NOT NULL DEFAULT 'normal' CHECK (bom_type IN ('normal', 'phantom', 'subcontract'));
ALTER TABLE boms ADD COLUMN routing_id uuid;
ALTER TABLE boms ADD COLUMN subcontractor_id uuid;
ALTER TABLE boms ADD COLUMN subcontract_price numeric(20, 4) NOT NULL DEFAULT 0 CHECK (subcontract_price >= 0);
ALTER TABLE boms ADD COLUMN lead_days int NOT NULL DEFAULT 0 CHECK (lead_days BETWEEN 0 AND 365);
ALTER TABLE boms ADD COLUMN version_note text NOT NULL DEFAULT '';
UPDATE boms SET family_id = id;
ALTER TABLE boms ADD CONSTRAINT boms_family_id_same_company FOREIGN KEY (company_id, family_id) REFERENCES boms (company_id, id);
ALTER TABLE boms ADD CONSTRAINT boms_routing_id_same_company FOREIGN KEY (company_id, routing_id) REFERENCES mfg_routings (company_id, id);
ALTER TABLE boms ADD CONSTRAINT boms_subcontractor_id_same_company FOREIGN KEY (company_id, subcontractor_id) REFERENCES suppliers (company_id, id);
ALTER TABLE boms ADD CONSTRAINT boms_subcontract_check CHECK (bom_type <> 'subcontract' OR subcontractor_id IS NOT NULL);
-- one active version per family
CREATE UNIQUE INDEX boms_one_active_version ON boms (family_id) WHERE is_active;
CREATE INDEX boms_item_idx ON boms (company_id, item_id);

CREATE TABLE bom_byproducts (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  bom_id     uuid NOT NULL,
  item_id    uuid NOT NULL,
  quantity   numeric(18, 4) NOT NULL CHECK (quantity > 0),
  cost_share numeric(6, 2) NOT NULL DEFAULT 0 CHECK (cost_share >= 0 AND cost_share < 100),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT bom_byproducts_bom_id_same_company FOREIGN KEY (company_id, bom_id) REFERENCES boms (company_id, id),
  CONSTRAINT bom_byproducts_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX bom_byproducts_bom_idx ON bom_byproducts (bom_id);

-- subcontractor's stock (components sent to them) lives in a warehouse of theirs
ALTER TABLE warehouses ADD COLUMN subcontractor_id uuid;
ALTER TABLE warehouses ADD CONSTRAINT warehouses_subcontractor_id_same_company FOREIGN KEY (company_id, subcontractor_id) REFERENCES suppliers (company_id, id);
CREATE UNIQUE INDEX warehouses_subcontractor_idx ON warehouses (subcontractor_id) WHERE subcontractor_id IS NOT NULL;

-- ---- manufacturing orders ----
ALTER TABLE mfg_orders DROP CONSTRAINT IF EXISTS mfg_orders_status_check;
ALTER TABLE mfg_orders ADD CONSTRAINT mfg_orders_status_check CHECK (status IN ('draft', 'confirmed', 'progress', 'done', 'cancelled'));
ALTER TABLE mfg_orders ADD COLUMN bom_type text NOT NULL DEFAULT 'normal';
ALTER TABLE mfg_orders ADD COLUMN routing_id uuid;
ALTER TABLE mfg_orders ADD COLUMN deadline date;
ALTER TABLE mfg_orders ADD COLUMN origin text NOT NULL DEFAULT '';
ALTER TABLE mfg_orders ADD COLUMN backorder_of uuid;
ALTER TABLE mfg_orders ADD COLUMN parent_id uuid;
ALTER TABLE mfg_orders ADD COLUMN sales_order_id uuid;
ALTER TABLE mfg_orders ADD COLUMN ops_cost numeric(20, 4) NOT NULL DEFAULT 0;
ALTER TABLE mfg_orders ADD COLUMN service_cost numeric(20, 4) NOT NULL DEFAULT 0;
ALTER TABLE mfg_orders ADD COLUMN byproducts_value numeric(20, 4) NOT NULL DEFAULT 0;
ALTER TABLE mfg_orders ADD COLUMN expected_cost numeric(20, 4) NOT NULL DEFAULT 0;
ALTER TABLE mfg_orders ADD COLUMN standard_cost numeric(20, 6) NOT NULL DEFAULT 0;
ALTER TABLE mfg_orders ADD COLUMN variance numeric(20, 4) NOT NULL DEFAULT 0;
ALTER TABLE mfg_orders ADD COLUMN subcontract_wh_id uuid;
ALTER TABLE mfg_orders ADD COLUMN components_sent boolean NOT NULL DEFAULT false;
ALTER TABLE mfg_orders ADD COLUMN service_bill_id uuid;
ALTER TABLE mfg_orders ADD COLUMN confirmed_at timestamptz;
ALTER TABLE mfg_orders ADD CONSTRAINT mfg_orders_routing_id_same_company FOREIGN KEY (company_id, routing_id) REFERENCES mfg_routings (company_id, id);
ALTER TABLE mfg_orders ADD CONSTRAINT mfg_orders_backorder_of_same_company FOREIGN KEY (company_id, backorder_of) REFERENCES mfg_orders (company_id, id);
ALTER TABLE mfg_orders ADD CONSTRAINT mfg_orders_parent_id_same_company FOREIGN KEY (company_id, parent_id) REFERENCES mfg_orders (company_id, id);
ALTER TABLE mfg_orders ADD CONSTRAINT mfg_orders_sales_order_id_same_company FOREIGN KEY (company_id, sales_order_id) REFERENCES sales_orders (company_id, id);
ALTER TABLE mfg_orders ADD CONSTRAINT mfg_orders_subcontract_wh_id_same_company FOREIGN KEY (company_id, subcontract_wh_id) REFERENCES warehouses (company_id, id);
ALTER TABLE mfg_orders ADD CONSTRAINT mfg_orders_service_bill_fk FOREIGN KEY (tenant_id, service_bill_id) REFERENCES bills (tenant_id, id);
CREATE INDEX mfg_orders_status_idx ON mfg_orders (company_id, status);

ALTER TABLE mfg_order_lines ADD COLUMN reserved_qty numeric(18, 4) NOT NULL DEFAULT 0 CHECK (reserved_qty >= 0);
ALTER TABLE mfg_order_lines ADD COLUMN sent_qty numeric(18, 4) NOT NULL DEFAULT 0 CHECK (sent_qty >= 0);
CREATE INDEX mfg_order_lines_item_idx ON mfg_order_lines (item_id);

CREATE TABLE mfg_order_byproducts (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  order_id     uuid NOT NULL,
  item_id      uuid NOT NULL,
  planned_qty  numeric(18, 4) NOT NULL CHECK (planned_qty >= 0),
  produced_qty numeric(18, 4) NOT NULL DEFAULT 0 CHECK (produced_qty >= 0),
  cost_share   numeric(6, 2) NOT NULL DEFAULT 0,
  value        numeric(20, 4) NOT NULL DEFAULT 0,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT mfg_order_byproducts_order_id_same_company FOREIGN KEY (company_id, order_id) REFERENCES mfg_orders (company_id, id),
  CONSTRAINT mfg_order_byproducts_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX mfg_order_byproducts_order_idx ON mfg_order_byproducts (order_id);

-- ---- work orders (one per routing operation) and their time logs ----
CREATE TABLE mfg_workorders (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  order_id      uuid NOT NULL,
  seq           int NOT NULL,
  name          text NOT NULL,
  workcenter_id uuid NOT NULL,
  instructions  text NOT NULL DEFAULT '',
  planned_min   numeric(12, 2) NOT NULL DEFAULT 0 CHECK (planned_min >= 0),
  actual_min    numeric(12, 2) NOT NULL DEFAULT 0 CHECK (actual_min >= 0),
  status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ready', 'progress', 'paused', 'done', 'cancelled')),
  qty_done      numeric(18, 4) NOT NULL DEFAULT 0 CHECK (qty_done >= 0),
  cost          numeric(20, 4) NOT NULL DEFAULT 0,
  operator_id   uuid REFERENCES users (id),
  started_at    timestamptz,
  finished_at   timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT mfg_workorders_order_id_same_company FOREIGN KEY (company_id, order_id) REFERENCES mfg_orders (company_id, id),
  CONSTRAINT mfg_workorders_workcenter_id_same_company FOREIGN KEY (company_id, workcenter_id) REFERENCES mfg_workcenters (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (order_id, seq)
);
CREATE INDEX mfg_workorders_wc_idx ON mfg_workorders (company_id, workcenter_id, status);

CREATE TABLE mfg_wo_times (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  workorder_id uuid NOT NULL,
  user_id      uuid DEFAULT app_user() REFERENCES users (id),
  started_at   timestamptz NOT NULL DEFAULT now(),
  ended_at     timestamptz,
  minutes      numeric(12, 2) NOT NULL DEFAULT 0 CHECK (minutes >= 0),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT mfg_wo_times_workorder_id_same_company FOREIGN KEY (company_id, workorder_id) REFERENCES mfg_workorders (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX mfg_wo_times_wo_idx ON mfg_wo_times (workorder_id);

-- ---- quality ----
CREATE TABLE mfg_quality_points (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  name          text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 160),
  trigger_on    text NOT NULL CHECK (trigger_on IN ('operation', 'production', 'receipt')),
  item_id       uuid,
  workcenter_id uuid,
  test_type     text NOT NULL DEFAULT 'passfail' CHECK (test_type IN ('passfail', 'measure')),
  norm          numeric(20, 4),
  tol_min       numeric(20, 4),
  tol_max       numeric(20, 4),
  unit          text NOT NULL DEFAULT '',
  instructions  text NOT NULL DEFAULT '',
  is_active     boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT mfg_quality_points_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  CONSTRAINT mfg_quality_points_workcenter_id_same_company FOREIGN KEY (company_id, workcenter_id) REFERENCES mfg_workcenters (company_id, id),
  UNIQUE (company_id, id),
  CHECK (test_type <> 'measure' OR (tol_min IS NOT NULL AND tol_max IS NOT NULL AND tol_max >= tol_min))
);

CREATE TABLE mfg_quality_checks (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  point_id     uuid NOT NULL,
  order_id     uuid,
  workorder_id uuid,
  receipt_id   uuid,
  item_id      uuid,
  status       text NOT NULL DEFAULT 'todo' CHECK (status IN ('todo', 'pass', 'fail', 'cancelled')),
  measured     numeric(20, 4),
  note         text NOT NULL DEFAULT '',
  checked_by   uuid REFERENCES users (id),
  checked_at   timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT mfg_quality_checks_point_id_same_company FOREIGN KEY (company_id, point_id) REFERENCES mfg_quality_points (company_id, id),
  CONSTRAINT mfg_quality_checks_order_id_same_company FOREIGN KEY (company_id, order_id) REFERENCES mfg_orders (company_id, id),
  CONSTRAINT mfg_quality_checks_workorder_id_same_company FOREIGN KEY (company_id, workorder_id) REFERENCES mfg_workorders (company_id, id),
  CONSTRAINT mfg_quality_checks_receipt_id_same_company FOREIGN KEY (company_id, receipt_id) REFERENCES goods_receipts (company_id, id),
  CONSTRAINT mfg_quality_checks_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX mfg_quality_checks_order_idx ON mfg_quality_checks (order_id) WHERE order_id IS NOT NULL;
CREATE INDEX mfg_quality_checks_wo_idx ON mfg_quality_checks (workorder_id) WHERE workorder_id IS NOT NULL;
CREATE INDEX mfg_quality_checks_status_idx ON mfg_quality_checks (company_id, status);

CREATE TABLE mfg_quality_alerts (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  number      bigint NOT NULL,
  title       text NOT NULL CHECK (length(trim(title)) BETWEEN 1 AND 200),
  check_id    uuid,
  item_id     uuid,
  order_id    uuid,
  priority    smallint NOT NULL DEFAULT 1 CHECK (priority BETWEEN 0 AND 3),
  stage       text NOT NULL DEFAULT 'new' CHECK (stage IN ('new', 'progress', 'done')),
  root_cause  text NOT NULL DEFAULT '',
  action      text NOT NULL DEFAULT '',
  assigned_to uuid REFERENCES users (id),
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  closed_at   timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT mfg_quality_alerts_check_id_same_company FOREIGN KEY (company_id, check_id) REFERENCES mfg_quality_checks (company_id, id),
  CONSTRAINT mfg_quality_alerts_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  CONSTRAINT mfg_quality_alerts_order_id_same_company FOREIGN KEY (company_id, order_id) REFERENCES mfg_orders (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);

-- scrap documents can belong to a manufacturing order
ALTER TABLE stock_docs ADD COLUMN mfg_order_id uuid;
ALTER TABLE stock_docs ADD CONSTRAINT stock_docs_mfg_order_id_same_company FOREIGN KEY (company_id, mfg_order_id) REFERENCES mfg_orders (company_id, id);

-- components sent to a subcontractor (moved between our warehouse and theirs)
ALTER TABLE stock_moves DROP CONSTRAINT IF EXISTS stock_moves_source_check;
ALTER TABLE stock_moves ADD CONSTRAINT stock_moves_source_check CHECK (source IN ('bill', 'bill_void', 'fuel', 'adjust', 'sale', 'sale_void',
  'stock_void', 'fuel_void', 'adjust_void', 'transfer', 'transfer_void', 'credit_note', 'credit_note_void', 'debit_note', 'debit_note_void',
  'delivery', 'delivery_void', 'pos', 'pos_void', 'pos_refund', 'pos_refund_void', 'count', 'landed_cost', 'mfg_in', 'mfg_out', 'scrap', 'scrap_void',
  'mfg_sub_out', 'mfg_sub_in'));

-- row security, grants and the audit trail
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['mfg_workcenters', 'mfg_routings', 'mfg_routing_ops', 'bom_byproducts', 'mfg_order_byproducts', 'mfg_workorders',
                           'mfg_wo_times', 'mfg_quality_points', 'mfg_quality_checks', 'mfg_quality_alerts'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
    IF t NOT IN ('mfg_wo_times', 'mfg_order_byproducts', 'bom_byproducts', 'mfg_routing_ops') THEN
      EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    END IF;
  END LOOP;
END
$$;
-- +goose StatementEnd
GRANT DELETE ON mfg_routing_ops, bom_byproducts, mfg_order_byproducts TO paxve_app;

-- +goose Down
ALTER TABLE stock_moves DROP CONSTRAINT IF EXISTS stock_moves_source_check;
ALTER TABLE stock_moves ADD CONSTRAINT stock_moves_source_check CHECK (source IN ('bill', 'bill_void', 'fuel', 'adjust', 'sale', 'sale_void',
  'stock_void', 'fuel_void', 'adjust_void', 'transfer', 'transfer_void', 'credit_note', 'credit_note_void', 'debit_note', 'debit_note_void',
  'delivery', 'delivery_void', 'pos', 'pos_void', 'pos_refund', 'pos_refund_void', 'count', 'landed_cost', 'mfg_in', 'mfg_out', 'scrap', 'scrap_void'));
ALTER TABLE stock_docs DROP COLUMN IF EXISTS mfg_order_id;
DROP TABLE IF EXISTS mfg_quality_alerts, mfg_quality_checks, mfg_quality_points, mfg_wo_times, mfg_workorders, mfg_order_byproducts;
ALTER TABLE mfg_order_lines DROP COLUMN IF EXISTS sent_qty, DROP COLUMN IF EXISTS reserved_qty;
ALTER TABLE mfg_orders DROP COLUMN IF EXISTS confirmed_at, DROP COLUMN IF EXISTS service_bill_id, DROP COLUMN IF EXISTS components_sent,
  DROP COLUMN IF EXISTS subcontract_wh_id, DROP COLUMN IF EXISTS variance, DROP COLUMN IF EXISTS standard_cost, DROP COLUMN IF EXISTS expected_cost,
  DROP COLUMN IF EXISTS byproducts_value, DROP COLUMN IF EXISTS service_cost, DROP COLUMN IF EXISTS ops_cost, DROP COLUMN IF EXISTS sales_order_id,
  DROP COLUMN IF EXISTS parent_id, DROP COLUMN IF EXISTS backorder_of, DROP COLUMN IF EXISTS origin, DROP COLUMN IF EXISTS deadline,
  DROP COLUMN IF EXISTS routing_id, DROP COLUMN IF EXISTS bom_type;
UPDATE mfg_orders SET status = 'draft' WHERE status IN ('confirmed', 'progress');
ALTER TABLE mfg_orders DROP CONSTRAINT IF EXISTS mfg_orders_status_check;
ALTER TABLE mfg_orders ADD CONSTRAINT mfg_orders_status_check CHECK (status IN ('draft', 'done', 'cancelled'));
DROP INDEX IF EXISTS warehouses_subcontractor_idx;
ALTER TABLE warehouses DROP COLUMN IF EXISTS subcontractor_id;
DROP TABLE IF EXISTS bom_byproducts;
DROP INDEX IF EXISTS boms_one_active_version;
DROP INDEX IF EXISTS boms_item_idx;
ALTER TABLE boms DROP CONSTRAINT IF EXISTS boms_subcontract_check;
ALTER TABLE boms DROP COLUMN IF EXISTS version_note, DROP COLUMN IF EXISTS lead_days, DROP COLUMN IF EXISTS subcontract_price,
  DROP COLUMN IF EXISTS subcontractor_id, DROP COLUMN IF EXISTS routing_id, DROP COLUMN IF EXISTS bom_type, DROP COLUMN IF EXISTS family_id,
  DROP COLUMN IF EXISTS version, DROP COLUMN IF EXISTS code;
DROP TABLE IF EXISTS mfg_routing_ops, mfg_routings, mfg_workcenters;
ALTER TABLE companies DROP COLUMN IF EXISTS mfg_subcontract_account_id, DROP COLUMN IF EXISTS mfg_variance_account_id;
ALTER TABLE items DROP COLUMN IF EXISTS standard_cost;
