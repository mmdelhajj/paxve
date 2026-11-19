-- Finishing stage: translated deferral names, subscription proration, goods receipts with 3-way matching and supplier
-- price lists, employee expense claims, custom fields, manufacturing (bills of materials, orders, scrap), water
-- utilities, rotating shifts, leave accrual history, clock-in photo / geofence, fingerprint devices (ZKTeco ADMS push),
-- online payments (gateways, payment intents), e-invoicing (ZATCA phase 2, Egypt ETA).
-- +goose Up

-- ---------- deferral names: store the document number and party so the screen renders them in the user's language ----------
ALTER TABLE deferral_schedules ADD COLUMN source_number bigint, ADD COLUMN party text NOT NULL DEFAULT '';
UPDATE deferral_schedules d SET source_number = i.number, party = c.name
  FROM invoices i JOIN customers c ON c.id = i.customer_id WHERE d.source_type = 'invoice' AND i.id = d.source_id;
UPDATE deferral_schedules d SET source_number = b.number, party = su.name
  FROM bills b JOIN suppliers su ON su.id = b.supplier_id WHERE d.source_type = 'bill' AND b.id = d.source_id;
UPDATE deferral_schedules d SET party = x.description FROM expenses x WHERE d.source_type = 'expense' AND x.id = d.source_id;

-- ---------- subscription proration (start / stop mid-month, plan changes) ----------
ALTER TABLE companies ADD COLUMN prorate_billing boolean NOT NULL DEFAULT false;
ALTER TABLE subscriptions ADD COLUMN end_date date;
CREATE TABLE subscription_changes (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id      uuid NOT NULL,
  subscription_id uuid NOT NULL,
  change_date     date NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('plan', 'stop')),
  old_plan_id     uuid,
  new_plan_id     uuid,
  old_fee         numeric(20, 4) NOT NULL DEFAULT 0,
  new_fee         numeric(20, 4) NOT NULL DEFAULT 0,
  days_left       int NOT NULL DEFAULT 0,
  days_in_month   int NOT NULL DEFAULT 30,
  amount          numeric(20, 4) NOT NULL DEFAULT 0, -- + charged, - credited
  currency        char(3) REFERENCES currencies (code),
  invoice_id      uuid,
  credit_note_id  uuid,
  created_by      uuid DEFAULT app_user(),
  created_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, subscription_id) REFERENCES subscriptions (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX subscription_changes_sub_idx ON subscription_changes (subscription_id, change_date);

-- ---------- goods receipts (between the purchase order and the bill) and 3-way matching ----------
ALTER TABLE purchase_order_lines ADD COLUMN received_qty numeric(18, 4) NOT NULL DEFAULT 0 CHECK (received_qty >= 0);
ALTER TABLE companies ADD COLUMN three_way_match text NOT NULL DEFAULT 'warn' CHECK (three_way_match IN ('off', 'warn', 'block'));
ALTER TABLE companies ADD COLUMN match_price_tolerance numeric(6, 2) NOT NULL DEFAULT 2 CHECK (match_price_tolerance >= 0);
CREATE TABLE goods_receipts (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  number       bigint NOT NULL,
  order_id     uuid NOT NULL,
  receipt_date date NOT NULL,
  delivery_ref text NOT NULL DEFAULT '',
  notes        text NOT NULL DEFAULT '',
  status       text NOT NULL DEFAULT 'done' CHECK (status IN ('done', 'cancelled')),
  created_by   uuid DEFAULT app_user(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT goods_receipts_order_same_company FOREIGN KEY (company_id, order_id) REFERENCES purchase_orders (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
CREATE TABLE goods_receipt_lines (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  receipt_id  uuid NOT NULL,
  po_line_id  uuid NOT NULL,
  quantity    numeric(18, 4) NOT NULL CHECK (quantity > 0),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT goods_receipt_lines_receipt_same_company FOREIGN KEY (company_id, receipt_id) REFERENCES goods_receipts (company_id, id),
  CONSTRAINT goods_receipt_lines_po_line_same_company FOREIGN KEY (company_id, po_line_id) REFERENCES purchase_order_lines (company_id, id)
);
CREATE TABLE supplier_prices (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  supplier_id   uuid NOT NULL,
  item_id       uuid NOT NULL,
  currency      char(3) NOT NULL REFERENCES currencies (code),
  price         numeric(20, 6) NOT NULL CHECK (price >= 0),
  min_qty       numeric(18, 4) NOT NULL DEFAULT 0 CHECK (min_qty >= 0),
  supplier_code text NOT NULL DEFAULT '',
  lead_days     int NOT NULL DEFAULT 0 CHECK (lead_days BETWEEN 0 AND 365),
  valid_from    date,
  valid_to      date,
  created_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, supplier_id) REFERENCES suppliers (tenant_id, id),
  FOREIGN KEY (tenant_id, item_id) REFERENCES items (tenant_id, id),
  UNIQUE (company_id, id),
  CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to >= valid_from)
);
CREATE INDEX supplier_prices_lookup_idx ON supplier_prices (company_id, item_id, supplier_id);

-- ---------- employee expense claims ----------
ALTER TABLE companies ADD COLUMN claims_payable_account_id uuid;
ALTER TABLE companies ADD CONSTRAINT companies_claims_payable_fk FOREIGN KEY (tenant_id, claims_payable_account_id) REFERENCES accounts (tenant_id, id);
CREATE TABLE expense_claims (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  employee_id      uuid NOT NULL,
  claim_date       date NOT NULL,
  category         text NOT NULL DEFAULT 'other',
  description      text NOT NULL CHECK (length(trim(description)) > 0),
  currency         char(3) NOT NULL REFERENCES currencies (code),
  amount           numeric(20, 4) NOT NULL CHECK (amount > 0),
  account_id       uuid,
  status           text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'paid', 'cancelled')),
  pay_via          text NOT NULL DEFAULT 'cash' CHECK (pay_via IN ('cash', 'payroll')),
  decided_by       uuid,
  decided_at       timestamptz,
  decision_note    text NOT NULL DEFAULT '',
  journal_entry_id uuid,
  paid_method_id   uuid,
  paid_entry_id    uuid,
  paid_at          timestamptz,
  payslip_id       uuid,
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, employee_id) REFERENCES employees (tenant_id, id),
  FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  FOREIGN KEY (tenant_id, paid_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
CREATE INDEX expense_claims_emp_idx ON expense_claims (company_id, employee_id, claim_date DESC);
ALTER TABLE attachments DROP CONSTRAINT IF EXISTS attachments_entity_type_check;
ALTER TABLE attachments ADD CONSTRAINT attachments_entity_type_check CHECK (entity_type IN ('invoice', 'bill', 'expense', 'journal', 'customer',
  'supplier', 'asset', 'purchase_order', 'sales_order', 'payment', 'supplier_payment', 'ticket', 'item', 'cheque', 'expense_claim',
  'goods_receipt', 'mfg_order', 'employee'));

-- ---------- custom fields ----------
CREATE TABLE custom_fields (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  entity      text NOT NULL CHECK (entity IN ('customers', 'suppliers', 'items', 'invoices', 'employees')),
  key         text NOT NULL CHECK (key ~ '^[a-z][a-z0-9_]{0,39}$'),
  label       text NOT NULL CHECK (length(trim(label)) > 0),
  label_i18n  jsonb NOT NULL DEFAULT '{}',
  type        text NOT NULL CHECK (type IN ('text', 'number', 'date', 'choice', 'bool')),
  options     text[] NOT NULL DEFAULT '{}',
  required    boolean NOT NULL DEFAULT false,
  show_in_list boolean NOT NULL DEFAULT true,
  position    int NOT NULL DEFAULT 0,
  is_active   boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, entity, key)
);
ALTER TABLE customers ADD COLUMN custom jsonb NOT NULL DEFAULT '{}';
ALTER TABLE suppliers ADD COLUMN custom jsonb NOT NULL DEFAULT '{}';
ALTER TABLE items ADD COLUMN custom jsonb NOT NULL DEFAULT '{}';
ALTER TABLE invoices ADD COLUMN custom jsonb NOT NULL DEFAULT '{}';
ALTER TABLE employees ADD COLUMN custom jsonb NOT NULL DEFAULT '{}';

-- ---------- manufacturing ----------
ALTER TABLE companies ADD COLUMN mfg_labour_account_id uuid, ADD COLUMN mfg_overhead_account_id uuid, ADD COLUMN scrap_account_id uuid;
ALTER TABLE companies ADD CONSTRAINT companies_mfg_labour_fk FOREIGN KEY (tenant_id, mfg_labour_account_id) REFERENCES accounts (tenant_id, id);
ALTER TABLE companies ADD CONSTRAINT companies_mfg_overhead_fk FOREIGN KEY (tenant_id, mfg_overhead_account_id) REFERENCES accounts (tenant_id, id);
ALTER TABLE companies ADD CONSTRAINT companies_scrap_fk FOREIGN KEY (tenant_id, scrap_account_id) REFERENCES accounts (tenant_id, id);
CREATE TABLE boms (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  item_id       uuid NOT NULL,
  name          text NOT NULL CHECK (length(trim(name)) > 0),
  output_qty    numeric(18, 4) NOT NULL DEFAULT 1 CHECK (output_qty > 0),
  labour_cost   numeric(20, 4) NOT NULL DEFAULT 0 CHECK (labour_cost >= 0),
  overhead_cost numeric(20, 4) NOT NULL DEFAULT 0 CHECK (overhead_cost >= 0),
  notes         text NOT NULL DEFAULT '',
  is_active     boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, item_id) REFERENCES items (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE TABLE bom_lines (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  bom_id     uuid NOT NULL,
  line_no    int NOT NULL,
  item_id    uuid NOT NULL,
  quantity   numeric(18, 4) NOT NULL CHECK (quantity > 0),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT bom_lines_bom_same_company FOREIGN KEY (company_id, bom_id) REFERENCES boms (company_id, id),
  FOREIGN KEY (tenant_id, item_id) REFERENCES items (tenant_id, id),
  UNIQUE (bom_id, line_no)
);
CREATE TABLE mfg_orders (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  bom_id           uuid NOT NULL,
  item_id          uuid NOT NULL,
  quantity         numeric(18, 4) NOT NULL CHECK (quantity > 0),
  warehouse_id     uuid NOT NULL,
  order_date       date NOT NULL,
  status           text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'done', 'cancelled')),
  components_value numeric(20, 4) NOT NULL DEFAULT 0,
  labour           numeric(20, 4) NOT NULL DEFAULT 0,
  overhead         numeric(20, 4) NOT NULL DEFAULT 0,
  scrap_qty        numeric(18, 4) NOT NULL DEFAULT 0 CHECK (scrap_qty >= 0),
  unit_cost        numeric(20, 6) NOT NULL DEFAULT 0,
  journal_entry_id uuid,
  notes            text NOT NULL DEFAULT '',
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  done_at          timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT mfg_orders_bom_same_company FOREIGN KEY (company_id, bom_id) REFERENCES boms (company_id, id),
  FOREIGN KEY (tenant_id, item_id) REFERENCES items (tenant_id, id),
  FOREIGN KEY (tenant_id, warehouse_id) REFERENCES warehouses (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
CREATE TABLE mfg_order_lines (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  order_id     uuid NOT NULL,
  line_no      int NOT NULL,
  item_id      uuid NOT NULL,
  planned_qty  numeric(18, 4) NOT NULL CHECK (planned_qty >= 0),
  consumed_qty numeric(18, 4) NOT NULL DEFAULT 0 CHECK (consumed_qty >= 0),
  value        numeric(20, 4) NOT NULL DEFAULT 0,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT mfg_order_lines_order_same_company FOREIGN KEY (company_id, order_id) REFERENCES mfg_orders (company_id, id),
  FOREIGN KEY (tenant_id, item_id) REFERENCES items (tenant_id, id),
  UNIQUE (order_id, line_no)
);
ALTER TABLE stock_moves DROP CONSTRAINT IF EXISTS stock_moves_source_check;
ALTER TABLE stock_moves ADD CONSTRAINT stock_moves_source_check CHECK (source IN ('bill', 'bill_void', 'fuel', 'adjust', 'sale', 'sale_void',
  'stock_void', 'fuel_void', 'adjust_void', 'transfer', 'transfer_void', 'credit_note', 'credit_note_void', 'debit_note', 'debit_note_void',
  'delivery', 'delivery_void', 'pos', 'pos_void', 'pos_refund', 'pos_refund_void', 'count', 'landed_cost', 'mfg_in', 'mfg_out', 'scrap', 'scrap_void'));
ALTER TABLE stock_docs DROP CONSTRAINT IF EXISTS stock_docs_kind_check;
ALTER TABLE stock_docs ADD CONSTRAINT stock_docs_kind_check CHECK (kind IN ('fuel', 'adjust', 'scrap'));

-- ---------- manufacturing app (on for new companies, off for existing ones until switched on) ----------
ALTER TABLE companies DROP CONSTRAINT companies_apps_known;
ALTER TABLE companies ADD CONSTRAINT companies_apps_known
  CHECK (apps <@ ARRAY['gen','acc','col','msg','stk','pur','net','sat','pos','hr','crm','svc','mfg']::text[]);
ALTER TABLE companies ALTER COLUMN apps SET DEFAULT '{gen,acc,col,msg,stk,pur,net,sat,pos,hr,crm,svc,mfg}';

-- ---------- water utilities (metered billing in m³, same engine as generators) ----------
ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_business_type_check;
ALTER TABLE companies ADD CONSTRAINT companies_business_type_check CHECK (business_type IN ('generator', 'internet', 'satellite', 'water'));

-- ---------- HR: rotating shifts, roster overrides, leave accrual history, clock-in photo / geofence, devices ----------
CREATE TABLE shift_rotations (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) > 0),
  pattern    text[] NOT NULL CHECK (cardinality(pattern) BETWEEN 1 AND 62), -- schedule id per day of the cycle, or 'off'
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (company_id, id)
);
ALTER TABLE employees ADD COLUMN rotation_id uuid, ADD COLUMN rotation_start date;
ALTER TABLE employees ADD CONSTRAINT employees_rotation_fk FOREIGN KEY (tenant_id, rotation_id) REFERENCES shift_rotations (tenant_id, id);
CREATE TABLE employee_shifts (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  employee_id uuid NOT NULL,
  work_date   date NOT NULL,
  schedule_id uuid, -- NULL = day off
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, employee_id) REFERENCES employees (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, schedule_id) REFERENCES work_schedules (tenant_id, id),
  UNIQUE (employee_id, work_date)
);
CREATE TABLE leave_accruals (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  employee_id uuid NOT NULL,
  type_id     uuid NOT NULL,
  year        int NOT NULL,
  period      text NOT NULL, -- YYYY-MM (monthly accrual), YYYY (yearly grant / carry) or a free key for adjustments
  kind        text NOT NULL CHECK (kind IN ('accrual', 'carry', 'adjust')),
  days        numeric(6, 2) NOT NULL,
  note        text NOT NULL DEFAULT '',
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, employee_id) REFERENCES employees (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, type_id) REFERENCES leave_types (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE UNIQUE INDEX leave_accruals_once_idx ON leave_accruals (employee_id, type_id, period, kind) WHERE kind IN ('accrual', 'carry');
ALTER TABLE companies ADD COLUMN clock_photo boolean NOT NULL DEFAULT false, ADD COLUMN clock_geofence boolean NOT NULL DEFAULT false,
  ADD COLUMN clock_lat numeric(9, 6), ADD COLUMN clock_lng numeric(9, 6), ADD COLUMN clock_radius_m int NOT NULL DEFAULT 200 CHECK (clock_radius_m BETWEEN 20 AND 50000);
CREATE TABLE attendance_photos (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  attendance_id uuid NOT NULL,
  kind          text NOT NULL CHECK (kind IN ('in', 'out')),
  mime          text NOT NULL DEFAULT 'image/jpeg',
  data          bytea NOT NULL CHECK (length(data) BETWEEN 1 AND 1048576),
  created_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (attendance_id, kind)
);
CREATE TABLE attendance_devices (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  serial     text NOT NULL CHECK (serial ~ '^[A-Za-z0-9_-]{4,40}$'),
  name       text NOT NULL DEFAULT '',
  allowed_ip text NOT NULL DEFAULT '', -- empty = any address; otherwise the device must connect from this IP
  is_active  boolean NOT NULL DEFAULT true,
  last_seen  timestamptz,
  last_ip    text NOT NULL DEFAULT '',
  punches    bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE UNIQUE INDEX attendance_devices_serial_idx ON attendance_devices (serial);

-- ---------- online payments ----------
CREATE TABLE payment_gateways (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  kind       text NOT NULL CHECK (kind IN ('stripe', 'hosted', 'transfer')),
  name       text NOT NULL CHECK (length(trim(name)) > 0),
  method_id  uuid NOT NULL, -- the cash box / bank account the money lands in
  test_mode  boolean NOT NULL DEFAULT true,
  config     jsonb NOT NULL DEFAULT '{}', -- secrets sealed with the server key
  instructions text NOT NULL DEFAULT '',
  is_active  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, method_id) REFERENCES payment_methods (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE TABLE payment_intents (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  gateway_id   uuid NOT NULL,
  customer_id  uuid NOT NULL,
  invoice_id   uuid,
  currency     char(3) NOT NULL REFERENCES currencies (code),
  amount       numeric(20, 4) NOT NULL CHECK (amount > 0),
  status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'review', 'paid', 'failed', 'rejected', 'expired')),
  provider_ref text NOT NULL DEFAULT '',
  reference    text NOT NULL DEFAULT '', -- transfer reference typed by the customer (Whish / OMT / bank)
  payer_phone  text NOT NULL DEFAULT '',
  token        text NOT NULL,
  payment_id   uuid,
  error        text NOT NULL DEFAULT '',
  decided_by   uuid,
  created_at   timestamptz NOT NULL DEFAULT now(),
  paid_at      timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT payment_intents_gateway_same_company FOREIGN KEY (company_id, gateway_id) REFERENCES payment_gateways (company_id, id),
  FOREIGN KEY (tenant_id, customer_id) REFERENCES customers (tenant_id, id),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoices (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE UNIQUE INDEX payment_intents_token_idx ON payment_intents (token);
CREATE INDEX payment_intents_status_idx ON payment_intents (company_id, status, created_at DESC);

-- ---------- e-invoicing (ZATCA phase 2, Egypt ETA) ----------
CREATE TABLE einvoice_config (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  kind       text NOT NULL CHECK (kind IN ('zatca', 'eta')),
  env        text NOT NULL DEFAULT 'sandbox' CHECK (env IN ('sandbox', 'simulation', 'production', 'preprod')),
  config     jsonb NOT NULL DEFAULT '{}', -- secrets (private key, CSID secret, client secret) sealed
  counter    bigint NOT NULL DEFAULT 0,  -- ZATCA invoice counter (ICV)
  last_hash  text NOT NULL DEFAULT '',   -- ZATCA previous invoice hash (PIH)
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, kind),
  UNIQUE (company_id, id)
);
CREATE TABLE einvoice_docs (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  invoice_id   uuid NOT NULL,
  kind         text NOT NULL CHECK (kind IN ('zatca', 'eta')),
  counter      bigint NOT NULL DEFAULT 0,
  doc_uuid     uuid NOT NULL,
  hash         text NOT NULL DEFAULT '',
  prev_hash    text NOT NULL DEFAULT '',
  payload      text NOT NULL,
  qr           text NOT NULL DEFAULT '',
  status       text NOT NULL DEFAULT 'generated' CHECK (status IN ('generated', 'reported', 'cleared', 'accepted', 'rejected', 'submitted', 'valid', 'invalid', 'error')),
  response     jsonb NOT NULL DEFAULT '{}',
  submitted_at timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoices (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (invoice_id, kind)
);

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['subscription_changes', 'goods_receipts', 'goods_receipt_lines', 'supplier_prices', 'expense_claims', 'custom_fields',
                           'boms', 'bom_lines', 'mfg_orders', 'mfg_order_lines', 'shift_rotations', 'employee_shifts', 'leave_accruals',
                           'attendance_photos', 'attendance_devices', 'payment_gateways', 'payment_intents', 'einvoice_config', 'einvoice_docs'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
    IF t NOT IN ('attendance_photos', 'einvoice_docs') THEN
      EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    END IF;
  END LOOP;
END
$$;
-- +goose StatementEnd
GRANT DELETE ON supplier_prices, bom_lines, employee_shifts, custom_fields, mfg_order_lines TO paxve_app;

-- +goose Down
SELECT 1;
