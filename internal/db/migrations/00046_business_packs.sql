-- Phase 6a: business packs for electronics, furniture, wholesale / distribution (van sales), restaurant / café,
-- pharmacy and factory, with the vertical features each one needs on top of the shared engine:
--   electronics: serials / IMEI on the till and on invoices, serial history, trade-in at the till (a tender whose
--                account is a clearing account; the used device enters stock at the agreed value)
--   furniture:   made-to-order items with options and price per m², deposits on sales orders, delivery + assembly
--                scheduling (delivery board trip + service job), showroom warehouses
--   wholesale:   salesman routes, vans as warehouses (load, sell offline from the phone, close the day with a count
--                and unload), salesman on invoices, commission report
--   restaurant:  tables, modifiers, open checks with kitchen lines (KDS by station), split, service charge, tips
--                (a liability), recipes = kits (ingredients leave stock on sale)
--   pharmacy:    generic name / active ingredient / strength / form, prescription and controlled flags, expired lots
--                blocked at sale (FEFO), prescription on the till sale, insurance share (a tender whose account is the
--                insurance receivable) with a claims register
-- +goose Up

ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_business_type_check;
ALTER TABLE companies ADD CONSTRAINT companies_business_type_check
  CHECK (business_type IN ('generator', 'internet', 'satellite', 'water', 'supermarket', 'clothing', 'electronics', 'furniture', 'wholesale',
                           'restaurant', 'pharmacy', 'factory'));
ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_pack_check;
ALTER TABLE companies ADD CONSTRAINT companies_pack_check
  CHECK (pack IN ('', 'supermarket', 'clothing', 'electronics', 'furniture', 'wholesale', 'restaurant', 'pharmacy', 'factory'));

ALTER TABLE companies
  ADD COLUMN tips_liability_account_id uuid,
  ADD COLUMN tradein_account_id uuid,
  ADD COLUMN insurance_account_id uuid,
  ADD COLUMN block_expired boolean NOT NULL DEFAULT false,
  ADD COLUMN expiry_alert_days int NOT NULL DEFAULT 90 CHECK (expiry_alert_days BETWEEN 1 AND 730),
  ADD COLUMN deposit_pct numeric(5, 2) NOT NULL DEFAULT 0 CHECK (deposit_pct BETWEEN 0 AND 100),
  ADD COLUMN service_charge_pct numeric(5, 2) NOT NULL DEFAULT 0 CHECK (service_charge_pct BETWEEN 0 AND 50);
ALTER TABLE companies ADD CONSTRAINT companies_tips_liab_fk FOREIGN KEY (tenant_id, tips_liability_account_id) REFERENCES accounts (tenant_id, id);
ALTER TABLE companies ADD CONSTRAINT companies_tradein_fk FOREIGN KEY (tenant_id, tradein_account_id) REFERENCES accounts (tenant_id, id);
ALTER TABLE companies ADD CONSTRAINT companies_insurance_fk FOREIGN KEY (tenant_id, insurance_account_id) REFERENCES accounts (tenant_id, id);

-- trade-in and insurance are tendered like a box whose account is the clearing / receivable account (till only)
ALTER TABLE payment_methods DROP CONSTRAINT payment_methods_kind_check;
ALTER TABLE payment_methods ADD CONSTRAINT payment_methods_kind_check
  CHECK (kind IN ('cash', 'wallet', 'bank', 'card', 'cheque', 'gift', 'loyalty', 'tradein', 'insurance'));

ALTER TABLE stock_moves DROP CONSTRAINT IF EXISTS stock_moves_source_check;
ALTER TABLE stock_moves ADD CONSTRAINT stock_moves_source_check CHECK (source IN ('bill', 'bill_void', 'fuel', 'adjust', 'sale', 'sale_void',
  'stock_void', 'fuel_void', 'adjust_void', 'transfer', 'transfer_void', 'credit_note', 'credit_note_void', 'debit_note', 'debit_note_void',
  'delivery', 'delivery_void', 'pos', 'pos_void', 'pos_refund', 'pos_refund_void', 'count', 'landed_cost', 'mfg_in', 'mfg_out', 'scrap', 'scrap_void',
  'mfg_sub_out', 'mfg_sub_in', 'tradein'));

-- ---------- items: furniture options, kitchen station, drug fields ----------
ALTER TABLE items
  ADD COLUMN made_to_order boolean NOT NULL DEFAULT false,
  ADD COLUMN options jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(options) = 'array' AND length(options::text) <= 20000), -- [{name, values: [{label, extra}]}]
  ADD COLUMN price_per_m2 numeric(20, 4) CHECK (price_per_m2 IS NULL OR price_per_m2 >= 0),
  ADD COLUMN kitchen_station text NOT NULL DEFAULT '' CHECK (length(kitchen_station) <= 40),
  ADD COLUMN generic_name text NOT NULL DEFAULT '' CHECK (length(generic_name) <= 200),
  ADD COLUMN active_ingredient text NOT NULL DEFAULT '' CHECK (length(active_ingredient) <= 200),
  ADD COLUMN strength text NOT NULL DEFAULT '' CHECK (length(strength) <= 60),
  ADD COLUMN dosage_form text NOT NULL DEFAULT '' CHECK (length(dosage_form) <= 60),
  ADD COLUMN rx_required boolean NOT NULL DEFAULT false,
  ADD COLUMN controlled boolean NOT NULL DEFAULT false;
CREATE INDEX items_ingredient_idx ON items (company_id, lower(active_ingredient)) WHERE active_ingredient <> '';

-- ---------- warehouses: showroom, van ----------
ALTER TABLE warehouses
  ADD COLUMN is_showroom boolean NOT NULL DEFAULT false,
  ADD COLUMN is_van boolean NOT NULL DEFAULT false,
  ADD COLUMN van_user_id uuid REFERENCES users (id);
CREATE INDEX warehouses_van_user_idx ON warehouses (company_id, van_user_id) WHERE is_van;

-- ---------- invoices: the salesman (van sales, commission) ----------
ALTER TABLE invoices ADD COLUMN salesman_id uuid REFERENCES users (id);
CREATE INDEX invoices_salesman_idx ON invoices (company_id, salesman_id, issue_date) WHERE salesman_id IS NOT NULL;

-- ---------- pos orders: what the verticals add to a till sale ----------
ALTER TABLE pos_orders
  ADD COLUMN serials jsonb NOT NULL DEFAULT '[]',   -- [{item_id, serial}]
  ADD COLUMN tradein jsonb NOT NULL DEFAULT '[]',   -- [{item_id, serial, value, note}]
  ADD COLUMN rx jsonb NOT NULL DEFAULT '{}',        -- {doctor, patient, number, date}
  ADD COLUMN insurance jsonb NOT NULL DEFAULT '{}', -- {insurer_id, insurer, policy_no, amount, claim_id}
  ADD COLUMN extras jsonb NOT NULL DEFAULT '{}',    -- {service_charge, tip, check_id, table}
  ADD COLUMN check_id uuid;

-- ---------- furniture: sales order deposits and delivery / assembly ----------
ALTER TABLE sales_orders
  ADD COLUMN deposit_pct numeric(5, 2) CHECK (deposit_pct IS NULL OR deposit_pct BETWEEN 0 AND 100), -- null = the company's
  ADD COLUMN delivery_date date,
  ADD COLUMN delivery_slot text NOT NULL DEFAULT '' CHECK (length(delivery_slot) <= 40),
  ADD COLUMN assembly boolean NOT NULL DEFAULT false,
  ADD COLUMN service_job_id uuid,
  ADD COLUMN trip_id uuid;
CREATE TABLE so_deposits (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  order_id   uuid NOT NULL,
  payment_id uuid NOT NULL,
  currency   char(3) NOT NULL REFERENCES currencies (code),
  amount     numeric(20, 4) NOT NULL CHECK (amount > 0),
  created_by uuid DEFAULT app_user(),
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT so_deposits_order_same_company FOREIGN KEY (company_id, order_id) REFERENCES sales_orders (company_id, id),
  CONSTRAINT so_deposits_payment_same_company FOREIGN KEY (company_id, payment_id) REFERENCES payments (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX so_deposits_order_idx ON so_deposits (order_id);

-- ---------- wholesale: routes and van days ----------
CREATE TABLE sales_routes (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  name         text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
  user_id      uuid REFERENCES users (id),
  warehouse_id uuid,
  weekdays     int[] NOT NULL DEFAULT '{}', -- 0 = Sunday … 6 = Saturday; empty = every day
  is_active    boolean NOT NULL DEFAULT true,
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT sales_routes_wh_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id),
  UNIQUE (company_id, id)
);
CREATE TABLE sales_route_stops (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  route_id    uuid NOT NULL,
  customer_id uuid NOT NULL,
  seq         int NOT NULL,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT sales_route_stops_route_same_company FOREIGN KEY (company_id, route_id) REFERENCES sales_routes (company_id, id) ON DELETE CASCADE,
  CONSTRAINT sales_route_stops_customer_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (route_id, customer_id)
);
CREATE TABLE van_days (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  number       bigint NOT NULL,
  warehouse_id uuid NOT NULL,
  source_warehouse_id uuid NOT NULL,
  user_id      uuid REFERENCES users (id),
  day          date NOT NULL DEFAULT current_date,
  status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  loaded       jsonb NOT NULL DEFAULT '[]',  -- [{item_id, quantity}]
  summary      jsonb NOT NULL DEFAULT '{}',  -- at close: sales, cash, differences, unloaded
  count_id     uuid,
  opened_at    timestamptz NOT NULL DEFAULT now(),
  closed_at    timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT van_days_wh_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id),
  CONSTRAINT van_days_src_same_company FOREIGN KEY (company_id, source_warehouse_id) REFERENCES warehouses (company_id, id),
  UNIQUE (company_id, id)
);
CREATE UNIQUE INDEX van_days_open_idx ON van_days (warehouse_id) WHERE status = 'open';

-- ---------- restaurant ----------
CREATE TABLE rest_tables (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 40),
  area       text NOT NULL DEFAULT '' CHECK (length(area) <= 40),
  seats      int NOT NULL DEFAULT 4 CHECK (seats BETWEEN 1 AND 100),
  x          int NOT NULL DEFAULT 0 CHECK (x BETWEEN 0 AND 100),
  y          int NOT NULL DEFAULT 0 CHECK (y BETWEEN 0 AND 100),
  shape      text NOT NULL DEFAULT 'square' CHECK (shape IN ('square', 'round', 'long')),
  is_active  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, name)
);
CREATE TABLE rest_modifiers (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  name         text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 80),
  name_i18n    jsonb NOT NULL DEFAULT '{}',
  grp          text NOT NULL DEFAULT '' CHECK (length(grp) <= 40),
  price        numeric(20, 4) NOT NULL DEFAULT 0 CHECK (price >= 0), -- in the base currency
  item_ids     uuid[] NOT NULL DEFAULT '{}',  -- empty item_ids and category_ids = every item
  category_ids uuid[] NOT NULL DEFAULT '{}',
  sort         int NOT NULL DEFAULT 0,
  is_active    boolean NOT NULL DEFAULT true,
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE TABLE rest_checks (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  number      bigint NOT NULL,
  register_id uuid NOT NULL,
  table_id    uuid,
  kind        text NOT NULL DEFAULT 'dine_in' CHECK (kind IN ('dine_in', 'takeaway', 'delivery')),
  status      text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'paid', 'cancelled')),
  guests      int NOT NULL DEFAULT 1 CHECK (guests BETWEEN 1 AND 500),
  customer_id uuid,
  name        text NOT NULL DEFAULT '' CHECK (length(name) <= 120),
  phone       text NOT NULL DEFAULT '' CHECK (length(phone) <= 40),
  address     text NOT NULL DEFAULT '' CHECK (length(address) <= 500),
  notes       text NOT NULL DEFAULT '' CHECK (length(notes) <= 500),
  waiter_id   uuid REFERENCES users (id) DEFAULT app_user(),
  pos_order_ids uuid[] NOT NULL DEFAULT '{}',
  opened_at   timestamptz NOT NULL DEFAULT now(),
  closed_at   timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT rest_checks_register_same_company FOREIGN KEY (company_id, register_id) REFERENCES pos_registers (company_id, id),
  CONSTRAINT rest_checks_table_same_company FOREIGN KEY (company_id, table_id) REFERENCES rest_tables (company_id, id),
  CONSTRAINT rest_checks_customer_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX rest_checks_open_idx ON rest_checks (company_id, status, opened_at);
CREATE TABLE rest_check_lines (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  check_id     uuid NOT NULL,
  item_id      uuid NOT NULL,
  quantity     numeric(14, 4) NOT NULL CHECK (quantity > 0),
  unit_price   numeric(20, 4) NOT NULL DEFAULT 0 CHECK (unit_price >= 0), -- in the register's currency, modifiers included
  modifiers    jsonb NOT NULL DEFAULT '[]', -- [{id, name, price}] price in the register's currency
  note         text NOT NULL DEFAULT '' CHECK (length(note) <= 200),
  station      text NOT NULL DEFAULT '' CHECK (length(station) <= 40),
  kstatus      text NOT NULL DEFAULT 'new' CHECK (kstatus IN ('new', 'sent', 'cooking', 'ready', 'served', 'void')),
  sent_at      timestamptz,
  ready_at     timestamptz,
  served_at    timestamptz,
  pos_order_id uuid,
  created_by   uuid DEFAULT app_user(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT rest_check_lines_check_same_company FOREIGN KEY (company_id, check_id) REFERENCES rest_checks (company_id, id),
  CONSTRAINT rest_check_lines_item_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX rest_check_lines_check_idx ON rest_check_lines (check_id, created_at);
CREATE INDEX rest_check_lines_kds_idx ON rest_check_lines (company_id, kstatus, sent_at) WHERE kstatus IN ('sent', 'cooking', 'ready');

-- ---------- pharmacy: insurance claims ----------
CREATE TABLE pharma_claims (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  number       bigint NOT NULL,
  pos_order_id uuid NOT NULL,
  insurer_id   uuid NOT NULL,
  patient      text NOT NULL DEFAULT '' CHECK (length(patient) <= 120),
  policy_no    text NOT NULL DEFAULT '' CHECK (length(policy_no) <= 60),
  currency     char(3) NOT NULL REFERENCES currencies (code),
  amount       numeric(20, 4) NOT NULL CHECK (amount > 0),
  status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'submitted', 'paid', 'rejected')),
  settled_entry_id uuid,
  settled_at   timestamptz,
  note         text NOT NULL DEFAULT '' CHECK (length(note) <= 500),
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, settled_entry_id) REFERENCES journal_entries (tenant_id, id),
  CONSTRAINT pharma_claims_order_same_company FOREIGN KEY (company_id, pos_order_id) REFERENCES pos_orders (company_id, id),
  CONSTRAINT pharma_claims_insurer_same_company FOREIGN KEY (company_id, insurer_id) REFERENCES customers (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX pharma_claims_status_idx ON pharma_claims (company_id, status, created_at);

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['so_deposits', 'sales_routes', 'sales_route_stops', 'van_days', 'rest_tables', 'rest_modifiers', 'rest_checks',
                           'rest_check_lines', 'pharma_claims'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
    IF t <> 'rest_check_lines' THEN
      EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    END IF;
  END LOOP;
END
$$;
-- +goose StatementEnd
GRANT DELETE ON sales_route_stops, rest_tables, rest_modifiers TO paxve_app;
CREATE TRIGGER so_deposits_immutable BEFORE UPDATE OR DELETE ON so_deposits FOR EACH ROW EXECUTE FUNCTION forbid_change();

-- +goose Down
SELECT 1;
