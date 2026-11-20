-- +goose Up
-- Odoo gaps (2026-11-20): customer credit limit, chatter (comments, mentions, history) and activities on any
-- record, in-app notifications, saved analysis (pivot) reports, repair orders, quotation templates, fiscal
-- positions, sales on instalments.

-- ---- credit limit ----
ALTER TABLE companies ADD COLUMN credit_policy text NOT NULL DEFAULT 'off' CHECK (credit_policy IN ('off', 'warn', 'block')),
  ADD COLUMN credit_overdue_days int CHECK (credit_overdue_days IS NULL OR credit_overdue_days BETWEEN 0 AND 3650);
ALTER TABLE customers ADD COLUMN credit_limit numeric(18, 4) CHECK (credit_limit IS NULL OR credit_limit >= 0),
  ADD COLUMN credit_overdue_days int CHECK (credit_overdue_days IS NULL OR credit_overdue_days BETWEEN 0 AND 3650),
  ADD COLUMN credit_hold boolean NOT NULL DEFAULT false;
ALTER TABLE approval_requests DROP CONSTRAINT IF EXISTS approval_requests_kind_check;
ALTER TABLE approval_requests ADD CONSTRAINT approval_requests_kind_check CHECK (kind IN ('void', 'credit_note', 'payment', 'refund', 'credit_limit'));

-- ---- chatter: comments and log notes on any record ----
CREATE TABLE record_messages (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  entity_type text NOT NULL CHECK (entity_type ~ '^[a-z_]{2,40}$'),
  entity_id   uuid NOT NULL,
  body        text NOT NULL CHECK (length(trim(body)) > 0 AND length(body) <= 5000),
  mentions    uuid[] NOT NULL DEFAULT '{}',
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  deleted_at  timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, created_by) REFERENCES users (tenant_id, id)
);
CREATE INDEX record_messages_entity_idx ON record_messages (company_id, entity_type, entity_id, created_at);

-- ---- in-app notifications (mentions, assigned activities, credit approvals…) ----
CREATE TABLE user_notifications (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  user_id     uuid NOT NULL,
  kind        text NOT NULL CHECK (kind IN ('mention', 'activity', 'activity_due', 'repair', 'system')),
  title       text NOT NULL CHECK (length(title) <= 300),
  body        text NOT NULL DEFAULT '' CHECK (length(body) <= 1000),
  entity_type text,
  entity_id   uuid,
  actor_id    uuid DEFAULT app_user(),
  read_at     timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id)
);
CREATE INDEX user_notifications_user_idx ON user_notifications (user_id, company_id, created_at DESC);
CREATE INDEX user_notifications_unread_idx ON user_notifications (user_id, company_id) WHERE read_at IS NULL;

-- ---- activities on any record (call, meeting, to-do, follow-up…) ----
CREATE TABLE record_activities (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  entity_type text CHECK (entity_type IS NULL OR entity_type ~ '^[a-z_]{2,40}$'),
  entity_id   uuid,
  kind        text NOT NULL DEFAULT 'todo' CHECK (kind IN ('call', 'meeting', 'todo', 'followup', 'email', 'visit')),
  summary     text NOT NULL CHECK (length(trim(summary)) > 0 AND length(summary) <= 300),
  note        text NOT NULL DEFAULT '' CHECK (length(note) <= 4000),
  due_date    date NOT NULL,
  assigned_to uuid NOT NULL,
  done_at     timestamptz,
  done_by     uuid,
  done_note   text NOT NULL DEFAULT '' CHECK (length(done_note) <= 4000),
  reminded_on date,
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, assigned_to) REFERENCES users (tenant_id, id),
  CHECK ((entity_type IS NULL) = (entity_id IS NULL))
);
CREATE INDEX record_activities_open_idx ON record_activities (company_id, assigned_to, due_date) WHERE done_at IS NULL;
CREATE INDEX record_activities_entity_idx ON record_activities (company_id, entity_type, entity_id);

-- ---- analysis: saved pivot / chart reports ----
CREATE TABLE analysis_reports (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) > 0 AND length(name) <= 120),
  spec       jsonb NOT NULL,
  shared     boolean NOT NULL DEFAULT false,
  owner_id   uuid NOT NULL DEFAULT app_user(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, owner_id) REFERENCES users (tenant_id, id)
);
CREATE INDEX analysis_reports_company_idx ON analysis_reports (company_id, owner_id);

-- ---- repair orders (device / serial, fault, parts + labour, warranty) ----
ALTER TABLE items ADD COLUMN warranty_months int NOT NULL DEFAULT 0 CHECK (warranty_months BETWEEN 0 AND 240);
CREATE TABLE repair_orders (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  number         bigint NOT NULL,
  customer_id    uuid NOT NULL,
  item_id        uuid,
  device         text NOT NULL DEFAULT '' CHECK (length(device) <= 200),
  serial         text NOT NULL DEFAULT '' CHECK (length(serial) <= 100),
  accessories    text NOT NULL DEFAULT '' CHECK (length(accessories) <= 500),
  fault          text NOT NULL CHECK (length(trim(fault)) > 0 AND length(fault) <= 2000),
  diagnosis      text NOT NULL DEFAULT '' CHECK (length(diagnosis) <= 4000),
  status         text NOT NULL DEFAULT 'received' CHECK (status IN ('received', 'diagnosing', 'waiting_parts', 'repaired', 'delivered', 'cancelled')),
  sale_date      date,
  warranty_until date,
  under_warranty boolean NOT NULL DEFAULT false,
  promised_date  date,
  currency       char(3) NOT NULL,
  notify         boolean NOT NULL DEFAULT true,
  invoice_id     uuid,
  warranty_doc   uuid,
  assigned_to    uuid,
  received_at    timestamptz NOT NULL DEFAULT now(),
  repaired_at    timestamptz,
  delivered_at   timestamptz,
  created_by     uuid DEFAULT app_user(),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number),
  CONSTRAINT repair_orders_customer_id_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  CONSTRAINT repair_orders_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  CONSTRAINT repair_orders_invoice_id_same_company FOREIGN KEY (company_id, invoice_id) REFERENCES invoices (company_id, id),
  FOREIGN KEY (tenant_id, assigned_to) REFERENCES users (tenant_id, id)
);
CREATE INDEX repair_orders_status_idx ON repair_orders (company_id, status, received_at);
CREATE INDEX repair_orders_serial_idx ON repair_orders (company_id, lower(serial)) WHERE serial <> '';
CREATE TABLE repair_lines (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  repair_id    uuid NOT NULL REFERENCES repair_orders (id) ON DELETE CASCADE,
  line_no      int NOT NULL,
  kind         text NOT NULL CHECK (kind IN ('part', 'labour')),
  item_id      uuid,
  warehouse_id uuid,
  description  text NOT NULL CHECK (length(trim(description)) > 0 AND length(description) <= 300),
  quantity     numeric(18, 4) NOT NULL CHECK (quantity > 0),
  unit_price   numeric(18, 4) NOT NULL CHECK (unit_price >= 0),
  covered      boolean NOT NULL DEFAULT false,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT repair_lines_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  CONSTRAINT repair_lines_warehouse_id_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id)
);
CREATE INDEX repair_lines_repair_idx ON repair_lines (repair_id, line_no);
ALTER TABLE stock_docs ADD COLUMN repair_id uuid;

-- ---- quotation templates ----
CREATE TABLE quote_templates (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  name          text NOT NULL CHECK (length(trim(name)) > 0 AND length(name) <= 120),
  currency      char(3),
  validity_days int CHECK (validity_days IS NULL OR validity_days BETWEEN 1 AND 3650),
  terms         text NOT NULL DEFAULT '' CHECK (length(terms) <= 8000),
  notes         text NOT NULL DEFAULT '' CHECK (length(notes) <= 4000),
  lines         jsonb NOT NULL DEFAULT '[]',
  is_active     boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id)
);

-- ---- fiscal positions: tax and account mapping per customer / supplier ----
CREATE TABLE fiscal_positions (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  name        text NOT NULL CHECK (length(trim(name)) > 0 AND length(name) <= 120),
  kind        text NOT NULL DEFAULT 'other' CHECK (kind IN ('exempt', 'export', 'reverse_charge', 'other')),
  note        text NOT NULL DEFAULT '' CHECK (length(note) <= 1000),
  tax_map     jsonb NOT NULL DEFAULT '[]',
  account_map jsonb NOT NULL DEFAULT '[]',
  is_active   boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id)
);
ALTER TABLE customers ADD COLUMN fiscal_position_id uuid,
  ADD CONSTRAINT customers_fiscal_position_id_same_company FOREIGN KEY (company_id, fiscal_position_id) REFERENCES fiscal_positions (company_id, id);
ALTER TABLE suppliers ADD COLUMN fiscal_position_id uuid,
  ADD CONSTRAINT suppliers_fiscal_position_id_same_company FOREIGN KEY (company_id, fiscal_position_id) REFERENCES fiscal_positions (company_id, id);
CREATE INDEX customers_fiscal_position_id_fk_idx ON customers (fiscal_position_id) WHERE fiscal_position_id IS NOT NULL;
CREATE INDEX suppliers_fiscal_position_id_fk_idx ON suppliers (fiscal_position_id) WHERE fiscal_position_id IS NOT NULL;

-- ---- sales on instalments ----
CREATE TABLE instalment_plans (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  number       bigint NOT NULL,
  invoice_id   uuid NOT NULL,
  customer_id  uuid NOT NULL,
  currency     char(3) NOT NULL,
  total        numeric(18, 4) NOT NULL CHECK (total > 0),
  down_payment numeric(18, 4) NOT NULL DEFAULT 0 CHECK (down_payment >= 0),
  months       int NOT NULL CHECK (months BETWEEN 1 AND 120),
  fee          numeric(18, 4) NOT NULL DEFAULT 0 CHECK (fee >= 0),
  first_due    date NOT NULL,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paid', 'cancelled')),
  notes        text NOT NULL DEFAULT '' CHECK (length(notes) <= 2000),
  created_by   uuid DEFAULT app_user(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number),
  CONSTRAINT instalment_plans_invoice_id_same_company FOREIGN KEY (company_id, invoice_id) REFERENCES invoices (company_id, id),
  CONSTRAINT instalment_plans_customer_id_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id)
);
CREATE UNIQUE INDEX instalment_plans_invoice_active_idx ON instalment_plans (invoice_id) WHERE status = 'active';
CREATE TABLE instalment_lines (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  plan_id    uuid NOT NULL REFERENCES instalment_plans (id) ON DELETE CASCADE,
  seq        int NOT NULL CHECK (seq >= 0),
  due_date   date NOT NULL,
  amount     numeric(18, 4) NOT NULL CHECK (amount > 0),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (plan_id, seq)
);

-- open receivable items: an invoice on instalments is split into its unpaid instalments (payments are applied
-- to the instalments in order), so aging and reminders see what is really late.
CREATE VIEW ar_open_items WITH (security_invoker = true) AS
  SELECT i.id, i.company_id, i.customer_id, i.currency, i.status, i.kind, i.issue_date, i.number, i.due_date, (i.total - i.paid) AS balance
  FROM invoices i
  WHERE NOT EXISTS (SELECT 1 FROM instalment_plans p WHERE p.invoice_id = i.id AND p.status = 'active')
  UNION ALL
  SELECT i.id, i.company_id, i.customer_id, i.currency, i.status, i.kind, i.issue_date, i.number, l.due_date,
         greatest(0, least(l.amount, l.cum - i.paid)) AS balance
  FROM invoices i
  JOIN instalment_plans p ON p.invoice_id = i.id AND p.status = 'active'
  JOIN (SELECT plan_id, due_date, amount, sum(amount) OVER (PARTITION BY plan_id ORDER BY seq) AS cum FROM instalment_lines) l ON l.plan_id = p.id;
GRANT SELECT ON ar_open_items TO paxve_app;

-- the invoice of an active plan is due on its first unpaid instalment; a fully paid invoice closes its plan
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION instalment_due_sync() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE d date;
BEGIN
  IF EXISTS (SELECT 1 FROM instalment_plans WHERE invoice_id = NEW.id AND status = 'active') THEN
    SELECT l.due_date INTO d FROM instalment_plans p
      JOIN (SELECT plan_id, seq, due_date, sum(amount) OVER (PARTITION BY plan_id ORDER BY seq) AS cum FROM instalment_lines) l ON l.plan_id = p.id
      WHERE p.invoice_id = NEW.id AND p.status = 'active' AND l.cum > NEW.paid ORDER BY l.seq LIMIT 1;
    IF d IS NOT NULL THEN
      NEW.due_date := greatest(d, NEW.issue_date);
    END IF;
    IF NEW.paid >= NEW.total THEN
      UPDATE instalment_plans SET status = 'paid', updated_at = now() WHERE invoice_id = NEW.id AND status = 'active';
    END IF;
  ELSIF NEW.paid < NEW.total AND OLD.paid >= OLD.total THEN
    -- a payment voided after the plan closed: the plan is open again
    UPDATE instalment_plans SET status = 'active', updated_at = now() WHERE invoice_id = NEW.id AND status = 'paid';
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER invoices_instalment_due BEFORE UPDATE OF paid ON invoices FOR EACH ROW EXECUTE FUNCTION instalment_due_sync();

-- row security, grants and the audit trail for the new tables
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['record_messages', 'user_notifications', 'record_activities', 'analysis_reports', 'repair_orders', 'repair_lines',
                           'quote_templates', 'fiscal_positions', 'instalment_plans', 'instalment_lines'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
    IF t NOT IN ('user_notifications', 'record_messages') THEN
      EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    END IF;
  END LOOP;
END
$$;
-- +goose StatementEnd
GRANT DELETE ON repair_lines, instalment_lines TO paxve_app;

-- change history for records that had none yet (the chatter "history" reads the audit trail)
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['sales_orders', 'purchase_orders', 'employees', 'tickets', 'assets'] LOOP
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = t AND relkind = 'r')
       AND NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = t || '_audit' AND tgrelid = t::regclass) THEN
      EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    END IF;
  END LOOP;
END
$$;
-- +goose StatementEnd
CREATE INDEX IF NOT EXISTS audit_log_entity_idx ON audit_log (entity_id, id) WHERE entity_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS audit_log_entity_idx;
DROP TRIGGER IF EXISTS invoices_instalment_due ON invoices;
DROP FUNCTION IF EXISTS instalment_due_sync();
DROP VIEW IF EXISTS ar_open_items;
DROP TABLE IF EXISTS instalment_lines, instalment_plans;
ALTER TABLE customers DROP COLUMN IF EXISTS fiscal_position_id;
ALTER TABLE suppliers DROP COLUMN IF EXISTS fiscal_position_id;
DROP TABLE IF EXISTS fiscal_positions, quote_templates;
ALTER TABLE stock_docs DROP COLUMN IF EXISTS repair_id;
DROP TABLE IF EXISTS repair_lines, repair_orders;
ALTER TABLE items DROP COLUMN IF EXISTS warranty_months;
DROP TABLE IF EXISTS analysis_reports, record_activities, user_notifications, record_messages;
ALTER TABLE approval_requests DROP CONSTRAINT IF EXISTS approval_requests_kind_check;
UPDATE approval_requests SET kind = 'payment' WHERE kind = 'credit_limit';
ALTER TABLE approval_requests ADD CONSTRAINT approval_requests_kind_check CHECK (kind IN ('void', 'credit_note', 'payment', 'refund'));
ALTER TABLE customers DROP COLUMN IF EXISTS credit_limit, DROP COLUMN IF EXISTS credit_overdue_days, DROP COLUMN IF EXISTS credit_hold;
ALTER TABLE companies DROP COLUMN IF EXISTS credit_policy, DROP COLUMN IF EXISTS credit_overdue_days;
