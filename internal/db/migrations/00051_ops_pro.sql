-- Phase 9: big-company gaps 6–10.
--   * bank statements: import OFX / MT940 / CAMT.053 / CSV (parsed on the server), duplicate lines refused by a
--     fingerprint, matching rules (memo / reference regex, amount, direction → book an expense, settle a customer /
--     supplier, or ignore), suggestions against open invoices and bills, one-click reconcile.
--   * card terminals: a card box can have a terminal (driver: simulator / manual); every card charge is recorded with
--     its approval code; the terminal batch is settled to the bank (gross − fees = net, fees to an expense account).
--   * restaurant QR ordering: each table has a QR link to the public menu; the guest's order waits for staff, who
--     accept it onto the table's check (sent to the kitchen).
--   * shipping carriers (manual / Aramex / DHL): rate quote, label, tracking number and status.
--   * e-mail from a record (ticket, quotation, invoice…): PDF + files attached, replies come back to the record.
--   * Gantt: task start dates and dependencies, planned start of work orders.

-- +goose Up

-- ======================= bank =======================
ALTER TABLE bank_statements ADD COLUMN format text NOT NULL DEFAULT 'manual' CHECK (format IN ('manual', 'csv', 'ofx', 'mt940', 'camt')),
  ADD COLUMN skipped int NOT NULL DEFAULT 0;
ALTER TABLE bank_statement_lines ADD COLUMN fingerprint text, ADD COLUMN partner text NOT NULL DEFAULT '',
  ADD COLUMN rule_id uuid, ADD COLUMN payment_id uuid, ADD COLUMN supplier_payment_id uuid;
CREATE UNIQUE INDEX bank_statement_lines_fp_idx ON bank_statement_lines (method_id, fingerprint) WHERE fingerprint IS NOT NULL;

CREATE TABLE bank_rules (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  name         text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120),
  method_id    uuid,                                   -- NULL = every bank box
  field        text NOT NULL DEFAULT 'any' CHECK (field IN ('any', 'description', 'reference', 'partner')),
  pattern      text NOT NULL DEFAULT '' CHECK (length(pattern) <= 200),   -- regular expression, case-insensitive ("" = any text)
  direction    text NOT NULL DEFAULT 'any' CHECK (direction IN ('any', 'in', 'out')),
  amount_min   numeric(20, 4),
  amount_max   numeric(20, 4),
  action       text NOT NULL CHECK (action IN ('account', 'customer', 'supplier', 'ignore')),
  account_id   uuid,                                   -- action account: the other side (bank fee, interest, rent…)
  customer_id  uuid,                                   -- action customer: money in settles this customer's invoices
  supplier_id  uuid,                                   -- action supplier: money out pays this supplier's bills
  label        text NOT NULL DEFAULT '' CHECK (length(label) <= 200),
  priority     int NOT NULL DEFAULT 100,
  is_active    boolean NOT NULL DEFAULT true,
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT bank_rules_method_same_company FOREIGN KEY (company_id, method_id) REFERENCES payment_methods (company_id, id),
  CONSTRAINT bank_rules_account_same_company FOREIGN KEY (company_id, account_id) REFERENCES accounts (company_id, id),
  CONSTRAINT bank_rules_customer_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  CONSTRAINT bank_rules_supplier_same_company FOREIGN KEY (company_id, supplier_id) REFERENCES suppliers (company_id, id),
  CHECK ((action = 'account') = (account_id IS NOT NULL)),
  CHECK ((action = 'customer') = (customer_id IS NOT NULL)),
  CHECK ((action = 'supplier') = (supplier_id IS NOT NULL)),
  UNIQUE (company_id, id)
);

-- ======================= card terminals =======================
ALTER TABLE payment_methods ADD COLUMN terminal_driver text NOT NULL DEFAULT '' CHECK (terminal_driver IN ('', 'simulator', 'manual')),
  ADD COLUMN terminal_ref text NOT NULL DEFAULT '' CHECK (length(terminal_ref) <= 60),
  ADD COLUMN card_fee_pct numeric(6, 3) NOT NULL DEFAULT 0 CHECK (card_fee_pct >= 0 AND card_fee_pct <= 20),
  ADD COLUMN card_fee_fixed numeric(20, 4) NOT NULL DEFAULT 0 CHECK (card_fee_fixed >= 0),
  ADD COLUMN settle_method_id uuid;

CREATE TABLE card_settlements (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  method_id        uuid NOT NULL,       -- the card box (clearing account)
  bank_method_id   uuid NOT NULL,       -- where the net arrives
  settle_date      date NOT NULL,
  currency         char(3) NOT NULL REFERENCES currencies (code),
  gross            numeric(20, 4) NOT NULL CHECK (gross > 0),
  fee              numeric(20, 4) NOT NULL DEFAULT 0 CHECK (fee >= 0),
  net              numeric(20, 4) NOT NULL CHECK (net > 0),
  txns             int NOT NULL DEFAULT 0,
  batch_ref        text NOT NULL DEFAULT '' CHECK (length(batch_ref) <= 60),
  status           text NOT NULL DEFAULT 'posted' CHECK (status IN ('posted', 'void')),
  journal_entry_id uuid NOT NULL,
  void_entry_id    uuid,
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT card_settlements_method_same_company FOREIGN KEY (company_id, method_id) REFERENCES payment_methods (company_id, id),
  CONSTRAINT card_settlements_bank_same_company FOREIGN KEY (company_id, bank_method_id) REFERENCES payment_methods (company_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);

CREATE TABLE card_txns (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  method_id     uuid NOT NULL,
  register_id   uuid,
  currency      char(3) NOT NULL REFERENCES currencies (code),
  amount        numeric(20, 4) NOT NULL CHECK (amount > 0),
  status        text NOT NULL CHECK (status IN ('approved', 'declined', 'voided')),
  approval_code text NOT NULL DEFAULT '' CHECK (length(approval_code) <= 20),
  rrn           text NOT NULL DEFAULT '' CHECK (length(rrn) <= 40),       -- terminal / acquirer reference
  card_brand    text NOT NULL DEFAULT '' CHECK (length(card_brand) <= 20),
  card_last4    text NOT NULL DEFAULT '' CHECK (card_last4 ~ '^[0-9]{0,4}$'),
  message       text NOT NULL DEFAULT '' CHECK (length(message) <= 200),
  pos_order_id  uuid,
  settlement_id uuid,
  created_by    uuid DEFAULT app_user(),
  created_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT card_txns_method_same_company FOREIGN KEY (company_id, method_id) REFERENCES payment_methods (company_id, id),
  CONSTRAINT card_txns_register_same_company FOREIGN KEY (company_id, register_id) REFERENCES pos_registers (company_id, id),
  CONSTRAINT card_txns_order_same_company FOREIGN KEY (company_id, pos_order_id) REFERENCES pos_orders (company_id, id),
  CONSTRAINT card_txns_settlement_same_company FOREIGN KEY (company_id, settlement_id) REFERENCES card_settlements (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX card_txns_open_idx ON card_txns (company_id, method_id, created_at) WHERE status = 'approved' AND settlement_id IS NULL;

-- ======================= restaurant QR ordering =======================
ALTER TABLE rest_tables ADD COLUMN qr_token text UNIQUE CHECK (qr_token IS NULL OR length(qr_token) >= 32);
ALTER TABLE companies ADD COLUMN qr_ordering boolean NOT NULL DEFAULT false, ADD COLUMN qr_register_id uuid;
CREATE TABLE qr_orders (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  number      bigint NOT NULL,
  table_id    uuid NOT NULL,
  name        text NOT NULL DEFAULT '' CHECK (length(name) <= 60),
  note        text NOT NULL DEFAULT '' CHECK (length(note) <= 300),
  lines       jsonb NOT NULL CHECK (jsonb_typeof(lines) = 'array' AND jsonb_array_length(lines) BETWEEN 1 AND 40),
  total       numeric(20, 4) NOT NULL DEFAULT 0,
  currency    char(3) NOT NULL REFERENCES currencies (code),
  status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'rejected')),
  check_id    uuid,
  ip          text NOT NULL DEFAULT '',
  decided_by  uuid REFERENCES users (id),
  decided_at  timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT qr_orders_table_same_company FOREIGN KEY (company_id, table_id) REFERENCES rest_tables (company_id, id),
  CONSTRAINT qr_orders_check_same_company FOREIGN KEY (company_id, check_id) REFERENCES rest_checks (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
CREATE INDEX qr_orders_pending_idx ON qr_orders (company_id, status, created_at);

-- the table of a QR link (only when QR ordering is on and the company uses the till)
-- +goose StatementBegin
CREATE FUNCTION qr_lookup(p_token text) RETURNS TABLE (tenant_id uuid, company_id uuid, table_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT t.tenant_id, t.company_id, t.id FROM rest_tables t JOIN companies c ON c.id = t.company_id JOIN tenants n ON n.id = t.tenant_id
  WHERE t.qr_token = p_token AND t.is_active AND c.is_active AND n.is_active AND c.qr_ordering AND 'pos' = ANY (c.apps)
    AND tenant_jobs_ok(n.id) AND length(p_token) >= 32
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION qr_lookup(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION qr_lookup(text) TO paxve_app;

-- ======================= shipping carriers =======================
CREATE TABLE carrier_accounts (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  carrier     text NOT NULL CHECK (carrier IN ('manual', 'aramex', 'dhl')),
  name        text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 80),
  config      jsonb NOT NULL DEFAULT '{}',          -- secrets sealed at rest (like message providers)
  is_active   boolean NOT NULL DEFAULT true,
  is_default  boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE TABLE shipments (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id      uuid NOT NULL,
  number          bigint NOT NULL,
  account_id      uuid NOT NULL,
  carrier         text NOT NULL,
  sales_order_id  uuid,
  shop_order_id   uuid,
  trip_stop_id    uuid,
  customer_id     uuid,
  recipient       text NOT NULL CHECK (length(btrim(recipient)) BETWEEN 1 AND 120),
  phone           text NOT NULL DEFAULT '' CHECK (length(phone) <= 40),
  address         text NOT NULL DEFAULT '' CHECK (length(address) <= 500),
  city            text NOT NULL DEFAULT '' CHECK (length(city) <= 80),
  country         char(2) NOT NULL DEFAULT 'LB',
  weight_kg       numeric(10, 3) NOT NULL DEFAULT 1 CHECK (weight_kg > 0 AND weight_kg <= 10000),
  pieces          int NOT NULL DEFAULT 1 CHECK (pieces BETWEEN 1 AND 999),
  contents        text NOT NULL DEFAULT '' CHECK (length(contents) <= 200),
  currency        char(3) NOT NULL REFERENCES currencies (code),
  cod_amount      numeric(20, 4) NOT NULL DEFAULT 0 CHECK (cod_amount >= 0),
  rate            numeric(20, 4),
  service         text NOT NULL DEFAULT '' CHECK (length(service) <= 40),
  tracking_no     text NOT NULL DEFAULT '' CHECK (length(tracking_no) <= 60),
  label_id        uuid,                                 -- attachment holding the label PDF
  status          text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'created', 'in_transit', 'delivered', 'exception', 'cancelled')),
  events          jsonb NOT NULL DEFAULT '[]',
  error           text NOT NULL DEFAULT '' CHECK (length(error) <= 500),
  checked_at      timestamptz,
  created_by      uuid DEFAULT app_user(),
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT shipments_account_same_company FOREIGN KEY (company_id, account_id) REFERENCES carrier_accounts (company_id, id),
  CONSTRAINT shipments_so_same_company FOREIGN KEY (company_id, sales_order_id) REFERENCES sales_orders (company_id, id),
  CONSTRAINT shipments_shop_same_company FOREIGN KEY (company_id, shop_order_id) REFERENCES shop_orders (company_id, id),
  CONSTRAINT shipments_stop_same_company FOREIGN KEY (company_id, trip_stop_id) REFERENCES trip_stops (company_id, id),
  CONSTRAINT shipments_customer_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
CREATE INDEX shipments_open_idx ON shipments (company_id, status) WHERE status IN ('created', 'in_transit', 'exception');
ALTER TABLE attachments DROP CONSTRAINT IF EXISTS attachments_entity_type_check;

-- ======================= e-mail from records =======================
ALTER TABLE messages ADD COLUMN attachment_ids uuid[] NOT NULL DEFAULT '{}', ADD COLUMN reply_to text NOT NULL DEFAULT '' CHECK (length(reply_to) <= 200),
  ADD COLUMN entity_type text, ADD COLUMN entity_id uuid;
ALTER TABLE companies ADD COLUMN reply_to_email text NOT NULL DEFAULT '' CHECK (length(reply_to_email) <= 200);
CREATE TABLE record_mails (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  entity_type text NOT NULL,
  entity_id   uuid NOT NULL,
  direction   text NOT NULL CHECK (direction IN ('out', 'in')),
  address     text NOT NULL CHECK (length(address) <= 200),
  cc          text NOT NULL DEFAULT '' CHECK (length(cc) <= 400),
  subject     text NOT NULL DEFAULT '' CHECK (length(subject) <= 300),
  body        text NOT NULL DEFAULT '' CHECK (length(body) <= 20000),
  files       jsonb NOT NULL DEFAULT '[]',       -- [{id, filename}]
  message_id  uuid,                              -- the queued e-mail (direction out)
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX record_mails_entity_idx ON record_mails (company_id, entity_type, entity_id, created_at);
-- one thread token per record: "[PX-<token>]" in the subject (or name+<token>@ in the address) brings replies back
CREATE TABLE record_mail_threads (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  entity_type text NOT NULL,
  entity_id   uuid NOT NULL,
  token       text NOT NULL CHECK (token ~ '^[a-z0-9]{10}$'),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, token),
  UNIQUE (company_id, entity_type, entity_id),
  UNIQUE (company_id, id)
);

-- ======================= Gantt =======================
ALTER TABLE project_tasks ADD COLUMN start_date date;
ALTER TABLE mfg_workorders ADD COLUMN planned_start timestamptz;
ALTER TABLE mfg_orders ADD COLUMN planned_start timestamptz;
CREATE TABLE project_task_deps (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  task_id     uuid NOT NULL,       -- this task starts after…
  depends_on  uuid NOT NULL,       -- …this one is finished
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT project_task_deps_task_same_company FOREIGN KEY (company_id, task_id) REFERENCES project_tasks (company_id, id),
  CONSTRAINT project_task_deps_dep_same_company FOREIGN KEY (company_id, depends_on) REFERENCES project_tasks (company_id, id),
  CHECK (task_id <> depends_on),
  UNIQUE (task_id, depends_on),
  UNIQUE (company_id, id)
);

-- new kinds of in-app notification
ALTER TABLE user_notifications DROP CONSTRAINT user_notifications_kind_check;
ALTER TABLE user_notifications ADD CONSTRAINT user_notifications_kind_check
  CHECK (kind IN ('mention', 'activity', 'activity_due', 'repair', 'system', 'task', 'service', 'sla', 'automation', 'fleet',
                  'shop', 'recruit', 'appraisal', 'esign', 'campaign', 'smart', 'qr_order', 'mail_reply', 'shipment'));

-- ======================= row security =======================
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['bank_rules', 'card_settlements', 'card_txns', 'qr_orders', 'carrier_accounts', 'shipments', 'record_mails',
                           'record_mail_threads', 'project_task_deps'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO paxve_app', t);
    IF t IN ('bank_rules', 'card_settlements', 'carrier_accounts', 'shipments') THEN
      EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    END IF;
  END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION IF EXISTS qr_lookup(text);
DROP TABLE IF EXISTS project_task_deps, record_mail_threads, record_mails, shipments, carrier_accounts, qr_orders, card_txns, card_settlements, bank_rules CASCADE;
ALTER TABLE mfg_orders DROP COLUMN IF EXISTS planned_start;
ALTER TABLE mfg_workorders DROP COLUMN IF EXISTS planned_start;
ALTER TABLE project_tasks DROP COLUMN IF EXISTS start_date;
ALTER TABLE companies DROP COLUMN IF EXISTS reply_to_email, DROP COLUMN IF EXISTS qr_ordering, DROP COLUMN IF EXISTS qr_register_id;
ALTER TABLE messages DROP COLUMN IF EXISTS attachment_ids, DROP COLUMN IF EXISTS reply_to, DROP COLUMN IF EXISTS entity_type, DROP COLUMN IF EXISTS entity_id;
ALTER TABLE rest_tables DROP COLUMN IF EXISTS qr_token;
ALTER TABLE payment_methods DROP COLUMN IF EXISTS terminal_driver, DROP COLUMN IF EXISTS terminal_ref, DROP COLUMN IF EXISTS card_fee_pct,
  DROP COLUMN IF EXISTS card_fee_fixed, DROP COLUMN IF EXISTS settle_method_id;
DROP INDEX IF EXISTS bank_statement_lines_fp_idx;
ALTER TABLE bank_statement_lines DROP COLUMN IF EXISTS fingerprint, DROP COLUMN IF EXISTS partner, DROP COLUMN IF EXISTS rule_id,
  DROP COLUMN IF EXISTS payment_id, DROP COLUMN IF EXISTS supplier_payment_id;
ALTER TABLE bank_statements DROP COLUMN IF EXISTS format, DROP COLUMN IF EXISTS skipped;
