-- Odoo 19 audit stage: purchase orders / RFQs, dunning levels with late fees, unrealised FX revaluation,
-- deferred revenue / prepaid expense schedules, attachments on any document, declining-balance depreciation.
-- +goose Up

-- ---------- purchase orders (request for quotation → confirmed order → billed) ----------
CREATE TABLE purchase_orders (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  number        bigint NOT NULL,
  supplier_id   uuid NOT NULL,
  order_date    date NOT NULL,
  expected_date date,
  currency      char(3) NOT NULL REFERENCES currencies (code),
  status        text NOT NULL DEFAULT 'rfq' CHECK (status IN ('rfq', 'sent', 'confirmed', 'partial', 'billed', 'cancelled')),
  supplier_ref  text NOT NULL DEFAULT '',
  notes         text NOT NULL DEFAULT '',
  total         numeric(20, 4) NOT NULL DEFAULT 0,
  cost_center_id uuid,
  confirmed_at  timestamptz,
  created_by    uuid DEFAULT app_user(),
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, supplier_id) REFERENCES suppliers (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
CREATE INDEX purchase_orders_list_idx ON purchase_orders (company_id, order_date DESC, number DESC);

CREATE TABLE purchase_order_lines (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  order_id     uuid NOT NULL,
  line_no      int NOT NULL,
  item_id      uuid,
  warehouse_id uuid,
  account_id   uuid,
  unit_id      uuid,
  description  text NOT NULL DEFAULT '',
  quantity     numeric(18, 4) NOT NULL CHECK (quantity > 0),
  unit_price   numeric(20, 6) NOT NULL CHECK (unit_price >= 0),
  tax_rate_id  uuid,
  billed_qty   numeric(18, 4) NOT NULL DEFAULT 0 CHECK (billed_qty >= 0),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT purchase_order_lines_order_same_company FOREIGN KEY (company_id, order_id) REFERENCES purchase_orders (company_id, id),
  FOREIGN KEY (tenant_id, item_id) REFERENCES items (tenant_id, id),
  FOREIGN KEY (tenant_id, warehouse_id) REFERENCES warehouses (tenant_id, id),
  FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (order_id, line_no)
);

ALTER TABLE bills ADD COLUMN purchase_order_id uuid;
ALTER TABLE bills ADD CONSTRAINT bills_purchase_order_fk FOREIGN KEY (tenant_id, purchase_order_id) REFERENCES purchase_orders (tenant_id, id);
ALTER TABLE bill_lines ADD COLUMN po_line_id uuid;
CREATE INDEX bills_po_idx ON bills (purchase_order_id) WHERE purchase_order_id IS NOT NULL;

-- ---------- dunning (payment reminders by level, late fee, service cut-off) ----------
ALTER TABLE companies ADD COLUMN dunning_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE companies ADD COLUMN late_fee_account_id uuid;
ALTER TABLE companies ADD CONSTRAINT companies_late_fee_account_fk FOREIGN KEY (tenant_id, late_fee_account_id) REFERENCES accounts (tenant_id, id);

CREATE TABLE dunning_levels (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  level         int NOT NULL CHECK (level BETWEEN 1 AND 9),
  name          text NOT NULL CHECK (length(trim(name)) > 0),
  days_overdue  int NOT NULL CHECK (days_overdue >= 0),
  send_message  boolean NOT NULL DEFAULT true,
  message       text NOT NULL DEFAULT '',
  fee_kind      text NOT NULL DEFAULT 'none' CHECK (fee_kind IN ('none', 'fixed', 'percent')),
  fee_amount    numeric(20, 4) NOT NULL DEFAULT 0 CHECK (fee_amount >= 0),
  fee_currency  char(3) REFERENCES currencies (code),
  min_balance   numeric(20, 4) NOT NULL DEFAULT 0 CHECK (min_balance >= 0),
  cut_service   boolean NOT NULL DEFAULT false,
  is_active     boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, level)
);

CREATE TABLE dunning_events (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  invoice_id     uuid NOT NULL,
  customer_id    uuid NOT NULL,
  level          int NOT NULL,
  days_overdue   int NOT NULL,
  balance        numeric(20, 4) NOT NULL,
  currency       char(3) NOT NULL,
  fee_invoice_id uuid,
  message_queued boolean NOT NULL DEFAULT false,
  service_cut    boolean NOT NULL DEFAULT false,
  created_by     uuid DEFAULT app_user(),
  created_at     timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoices (tenant_id, id),
  UNIQUE (invoice_id, level)
);
CREATE INDEX dunning_events_customer_idx ON dunning_events (company_id, customer_id, created_at DESC);

-- ---------- unrealised exchange revaluation (month end, reversed on the 1st) ----------
CREATE TABLE fx_revaluations (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  period       text NOT NULL CHECK (period ~ '^\d{4}-(0[1-9]|1[0-2])$'),
  entry_id     uuid NOT NULL,
  reversal_id  uuid,
  total_gain   numeric(20, 4) NOT NULL DEFAULT 0,
  total_loss   numeric(20, 4) NOT NULL DEFAULT 0,
  details      jsonb NOT NULL DEFAULT '[]',
  created_by   uuid DEFAULT app_user(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, entry_id) REFERENCES journal_entries (tenant_id, id),
  FOREIGN KEY (tenant_id, reversal_id) REFERENCES journal_entries (tenant_id, id)
);
CREATE UNIQUE INDEX fx_revaluations_period_idx ON fx_revaluations (company_id, period);

-- ---------- deferred revenue / prepaid expenses ----------
ALTER TABLE companies ADD COLUMN deferred_revenue_account_id uuid;
ALTER TABLE companies ADD COLUMN prepaid_expense_account_id uuid;
ALTER TABLE companies ADD CONSTRAINT companies_deferred_rev_fk FOREIGN KEY (tenant_id, deferred_revenue_account_id) REFERENCES accounts (tenant_id, id);
ALTER TABLE companies ADD CONSTRAINT companies_prepaid_exp_fk FOREIGN KEY (tenant_id, prepaid_expense_account_id) REFERENCES accounts (tenant_id, id);
CREATE TABLE deferral_schedules (
  id                  uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id           uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id          uuid NOT NULL,
  number              bigint NOT NULL,
  kind                text NOT NULL CHECK (kind IN ('revenue', 'expense')),
  name                text NOT NULL CHECK (length(trim(name)) > 0),
  source_type         text NOT NULL DEFAULT 'manual' CHECK (source_type IN ('manual', 'invoice', 'bill', 'expense')),
  source_id           uuid,
  deferral_account_id uuid NOT NULL,
  target_account_id   uuid NOT NULL,
  amount_base         numeric(20, 4) NOT NULL CHECK (amount_base > 0),
  months              int NOT NULL CHECK (months BETWEEN 2 AND 120),
  start_period        text NOT NULL CHECK (start_period ~ '^\d{4}-(0[1-9]|1[0-2])$'),
  cost_center_id      uuid,
  status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'done', 'cancelled')),
  setup_entry_id      uuid,
  notes               text NOT NULL DEFAULT '',
  created_by          uuid DEFAULT app_user(),
  created_at          timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, deferral_account_id) REFERENCES accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, target_account_id) REFERENCES accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, setup_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);

CREATE TABLE deferral_lines (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  schedule_id uuid NOT NULL,
  period      text NOT NULL,
  amount      numeric(20, 4) NOT NULL CHECK (amount > 0),
  entry_id    uuid NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT deferral_lines_schedule_same_company FOREIGN KEY (company_id, schedule_id) REFERENCES deferral_schedules (company_id, id),
  FOREIGN KEY (tenant_id, entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (schedule_id, period)
);

-- ---------- attachments on any document ----------
CREATE TABLE attachments (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  entity_type text NOT NULL CHECK (entity_type IN ('invoice', 'bill', 'expense', 'journal', 'customer', 'supplier', 'asset',
                'purchase_order', 'sales_order', 'payment', 'supplier_payment', 'ticket', 'item', 'cheque')),
  entity_id   uuid NOT NULL,
  filename    text NOT NULL CHECK (length(trim(filename)) BETWEEN 1 AND 200),
  mime        text NOT NULL DEFAULT 'application/octet-stream',
  size        int NOT NULL CHECK (size BETWEEN 1 AND 10485760),
  data        bytea NOT NULL,
  note        text NOT NULL DEFAULT '',
  deleted_at  timestamptz,
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id)
);
CREATE INDEX attachments_entity_idx ON attachments (company_id, entity_type, entity_id) WHERE deleted_at IS NULL;

-- ---------- declining-balance depreciation ----------
ALTER TABLE assets ADD COLUMN method text NOT NULL DEFAULT 'straight_line' CHECK (method IN ('straight_line', 'declining'));
ALTER TABLE assets ADD COLUMN declining_factor numeric(6, 3) NOT NULL DEFAULT 2 CHECK (declining_factor > 0 AND declining_factor <= 5);

-- ---------- audit findings: goods revenue account, payment terms, new default accounts ----------
ALTER TABLE companies ADD COLUMN goods_revenue_account_id uuid;
ALTER TABLE companies ADD CONSTRAINT companies_goods_rev_fk FOREIGN KEY (tenant_id, goods_revenue_account_id) REFERENCES accounts (tenant_id, id);
ALTER TABLE companies ADD COLUMN default_due_days int NOT NULL DEFAULT 0 CHECK (default_due_days BETWEEN 0 AND 365);
ALTER TABLE customers ADD COLUMN due_days int CHECK (due_days IS NULL OR due_days BETWEEN 0 AND 365);
ALTER TABLE suppliers ADD COLUMN due_days int CHECK (due_days IS NULL OR due_days BETWEEN 0 AND 365);

-- existing Lebanese (PCG) and generic charts get prepaid / deferred accounts, then the defaults are pointed at them
INSERT INTO accounts (tenant_id, company_id, code, name, name_i18n, type, parent_id, is_postable)
SELECT c.tenant_id, c.id, x.code, x.name, x.i18n::jsonb, x.type, p.id, true
FROM companies c
JOIN (VALUES ('lebanon', '486', 'Prepaid expenses', '{"en":"Prepaid expenses","ar":"أعباء مدفوعة مسبقاً","fr":"Charges constatées d''avance"}', 'asset', '4'),
             ('lebanon', '487', 'Deferred revenue (paid in advance)', '{"en":"Deferred revenue (paid in advance)","ar":"إيرادات مقبوضة مسبقاً","fr":"Produits constatés d''avance"}', 'liability', '4'),
             ('generic', '2500', 'Deferred revenue', '{"en":"Deferred revenue","ar":"إيرادات مقبوضة مسبقاً","fr":"Produits constatés d''avance"}', 'liability', '2000'))
  AS x (chart, code, name, i18n, type, parent) ON true
JOIN accounts p ON p.company_id = c.id AND p.code = x.parent
WHERE ((x.chart = 'lebanon' AND EXISTS (SELECT 1 FROM accounts a WHERE a.company_id = c.id AND a.code = '467'))
    OR (x.chart = 'generic' AND EXISTS (SELECT 1 FROM accounts a WHERE a.company_id = c.id AND a.code = '2400')))
  AND NOT EXISTS (SELECT 1 FROM accounts a WHERE a.company_id = c.id AND a.code = x.code);

UPDATE companies c SET
  goods_revenue_account_id = coalesce(goods_revenue_account_id, (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('707', '4100') ORDER BY a.code LIMIT 1)),
  deferred_revenue_account_id = coalesce(deferred_revenue_account_id, (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('487', '2500') ORDER BY a.code LIMIT 1)),
  prepaid_expense_account_id = coalesce(prepaid_expense_account_id, (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('486', '1500') AND a.type = 'asset' ORDER BY a.code LIMIT 1)),
  late_fee_account_id = coalesce(late_fee_account_id, (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('77', '4900') AND a.type = 'income' ORDER BY a.code LIMIT 1));

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['purchase_orders', 'purchase_order_lines', 'dunning_levels', 'dunning_events', 'fx_revaluations',
                           'deferral_schedules', 'deferral_lines', 'attachments'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
    IF t <> 'attachments' THEN
      EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    END IF;
  END LOOP;
END
$$;
-- +goose StatementEnd
GRANT DELETE ON purchase_order_lines, dunning_levels TO paxve_app;

-- +goose Down
SELECT 1;
