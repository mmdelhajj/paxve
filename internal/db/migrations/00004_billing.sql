-- Subscribers, plans, meter readings, monthly invoices, payments (any currency, with change).
-- Rules enforced here:
--   * one invoice per subscription per period (unless voided)
--   * invoice amounts never change after issue; an invoice is voided (journal reversed), not deleted
--   * an invoice can never be paid more than its total (paid <= total)
--   * document numbers are gap-free per company and document type

-- +goose Up

CREATE TABLE doc_counters (
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  doc_type    text NOT NULL,
  last_number bigint NOT NULL,
  PRIMARY KEY (tenant_id, company_id, doc_type),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id)
);

-- +goose StatementBegin
CREATE FUNCTION next_doc_number(p_company uuid, p_type text) RETURNS bigint
LANGUAGE sql AS $$
  INSERT INTO doc_counters (tenant_id, company_id, doc_type, last_number)
  VALUES (app_tenant(), p_company, p_type, 1)
  ON CONFLICT (tenant_id, company_id, doc_type) DO UPDATE SET last_number = doc_counters.last_number + 1
  RETURNING last_number
$$;
-- +goose StatementEnd

-- Default accounts used by billing.
ALTER TABLE companies
  ADD COLUMN ar_account_id uuid,
  ADD COLUMN fx_gain_account_id uuid,
  ADD COLUMN fx_loss_account_id uuid,
  ADD COLUMN default_revenue_account_id uuid,
  ADD FOREIGN KEY (tenant_id, ar_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, fx_gain_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, fx_loss_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, default_revenue_account_id) REFERENCES accounts (tenant_id, id);

-- How money is received: cash boxes per currency, wallets (Whish, OMT), banks, cards.
CREATE TABLE payment_methods (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) > 0),
  kind       text NOT NULL DEFAULT 'cash' CHECK (kind IN ('cash', 'wallet', 'bank', 'card')),
  currency   char(3) NOT NULL REFERENCES currencies (code),
  account_id uuid NOT NULL,
  is_active  boolean NOT NULL DEFAULT true,
  sort       int NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, name)
);

CREATE TABLE areas (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  name         text NOT NULL CHECK (length(trim(name)) > 0),
  collector_id uuid,
  is_active    boolean NOT NULL DEFAULT true,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, collector_id) REFERENCES users (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, name)
);

-- Plans: fixed monthly fee (e.g. 5A, internet 20 Mbps) or metered (base fee + kWh x price).
CREATE TABLE plans (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id          uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id         uuid NOT NULL,
  name               text NOT NULL CHECK (length(trim(name)) > 0),
  billing_type       text NOT NULL CHECK (billing_type IN ('fixed', 'metered')),
  currency           char(3) NOT NULL REFERENCES currencies (code),
  base_fee           numeric(20, 4) NOT NULL DEFAULT 0 CHECK (base_fee >= 0),
  unit_price         numeric(20, 4) NOT NULL DEFAULT 0 CHECK (unit_price >= 0),
  unit_label         text NOT NULL DEFAULT 'kWh',
  revenue_account_id uuid NOT NULL,
  is_active          boolean NOT NULL DEFAULT true,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, revenue_account_id) REFERENCES accounts (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, name)
);

CREATE TABLE customers (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  code       text NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) > 0),
  phone      text NOT NULL DEFAULT '',
  phone2     text NOT NULL DEFAULT '',
  email      text NOT NULL DEFAULT '',
  address    text NOT NULL DEFAULT '',
  area_id    uuid,
  notes      text NOT NULL DEFAULT '',
  status     text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'closed')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, area_id) REFERENCES areas (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, code)
);
CREATE INDEX customers_name_idx ON customers (tenant_id, company_id, lower(name));
CREATE INDEX customers_phone_idx ON customers (tenant_id, company_id, phone);

CREATE TABLE subscriptions (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id      uuid NOT NULL,
  customer_id     uuid NOT NULL,
  plan_id         uuid NOT NULL,
  meter_no        text NOT NULL DEFAULT '',
  start_date      date NOT NULL DEFAULT current_date,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'ended')),
  fee_override    numeric(20, 4) CHECK (fee_override >= 0),
  initial_reading numeric(14, 2) NOT NULL DEFAULT 0 CHECK (initial_reading >= 0),
  notes           text NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, customer_id) REFERENCES customers (tenant_id, id),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES plans (tenant_id, id),
  UNIQUE (tenant_id, id)
);
CREATE INDEX subscriptions_customer_idx ON subscriptions (tenant_id, customer_id);

CREATE TABLE meter_readings (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  subscription_id uuid NOT NULL,
  period          text NOT NULL CHECK (period ~ '^\d{4}-(0[1-9]|1[0-2])$'),
  reading         numeric(14, 2) NOT NULL CHECK (reading >= 0),
  read_at         timestamptz NOT NULL DEFAULT now(),
  read_by         uuid DEFAULT app_user(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, subscription_id) REFERENCES subscriptions (tenant_id, id),
  UNIQUE (tenant_id, subscription_id, period)
);

CREATE TABLE invoices (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  customer_id      uuid NOT NULL,
  subscription_id  uuid,
  period           text CHECK (period ~ '^\d{4}-(0[1-9]|1[0-2])$'),
  issue_date       date NOT NULL,
  due_date         date NOT NULL,
  currency         char(3) NOT NULL REFERENCES currencies (code),
  total            numeric(20, 4) NOT NULL CHECK (total > 0),
  paid             numeric(20, 4) NOT NULL DEFAULT 0,
  status           text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'partial', 'paid', 'void')),
  journal_entry_id uuid,
  void_entry_id    uuid,
  notes            text NOT NULL DEFAULT '',
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, customer_id) REFERENCES customers (tenant_id, id),
  FOREIGN KEY (tenant_id, subscription_id) REFERENCES subscriptions (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  FOREIGN KEY (tenant_id, void_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, number),
  CHECK (paid >= 0 AND paid <= total),
  CHECK (due_date >= issue_date)
);
CREATE UNIQUE INDEX invoices_one_per_period ON invoices (tenant_id, subscription_id, period)
  WHERE status <> 'void' AND subscription_id IS NOT NULL;
CREATE INDEX invoices_customer_idx ON invoices (tenant_id, customer_id, issue_date);
CREATE INDEX invoices_period_idx ON invoices (tenant_id, company_id, period);

CREATE TABLE invoice_lines (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  invoice_id uuid NOT NULL,
  line_no    int NOT NULL,
  kind       text NOT NULL DEFAULT 'other' CHECK (kind IN ('fee', 'usage', 'other')),
  description text NOT NULL DEFAULT '',
  quantity   numeric(14, 4) NOT NULL DEFAULT 1,
  unit_price numeric(20, 4) NOT NULL DEFAULT 0,
  amount     numeric(20, 4) NOT NULL,
  meta       jsonb NOT NULL DEFAULT '{}'::jsonb,
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoices (tenant_id, id),
  UNIQUE (tenant_id, invoice_id, line_no)
);

CREATE TABLE payments (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  customer_id      uuid NOT NULL,
  payment_date     date NOT NULL,
  currency         char(3) NOT NULL REFERENCES currencies (code),
  amount           numeric(20, 4) NOT NULL CHECK (amount > 0),
  change_method_id uuid,
  change_currency  char(3) REFERENCES currencies (code),
  change_amount    numeric(20, 4) NOT NULL DEFAULT 0 CHECK (change_amount >= 0),
  status           text NOT NULL DEFAULT 'posted' CHECK (status IN ('posted', 'void')),
  journal_entry_id uuid,
  void_entry_id    uuid,
  notes            text NOT NULL DEFAULT '',
  received_by      uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, customer_id) REFERENCES customers (tenant_id, id),
  FOREIGN KEY (tenant_id, change_method_id) REFERENCES payment_methods (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  FOREIGN KEY (tenant_id, void_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, number)
);
CREATE INDEX payments_customer_idx ON payments (tenant_id, customer_id, payment_date);

CREATE TABLE payment_tenders (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  payment_id uuid NOT NULL,
  method_id  uuid NOT NULL,
  currency   char(3) NOT NULL REFERENCES currencies (code),
  amount     numeric(20, 4) NOT NULL CHECK (amount > 0),
  rate       numeric(28, 10) NOT NULL CHECK (rate > 0),
  FOREIGN KEY (tenant_id, payment_id) REFERENCES payments (tenant_id, id),
  FOREIGN KEY (tenant_id, method_id) REFERENCES payment_methods (tenant_id, id)
);

-- Allocations link payments to invoices. A negative allocation undoes one (when a payment is voided).
CREATE TABLE payment_allocations (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  payment_id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  amount     numeric(20, 4) NOT NULL CHECK (amount <> 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, payment_id) REFERENCES payments (tenant_id, id),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoices (tenant_id, id)
);
CREATE INDEX payment_allocations_invoice_idx ON payment_allocations (tenant_id, invoice_id);

-- Keep invoices.paid and status in step with allocations (paid <= total is a CHECK on invoices).
-- +goose StatementBegin
CREATE FUNCTION apply_allocation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  UPDATE invoices
  SET paid = paid + NEW.amount,
      status = CASE
        WHEN status = 'void' THEN 'void'
        WHEN paid + NEW.amount = 0 THEN 'open'
        WHEN paid + NEW.amount >= total THEN 'paid'
        ELSE 'partial' END
  WHERE id = NEW.invoice_id;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER payment_allocations_apply AFTER INSERT ON payment_allocations
  FOR EACH ROW EXECUTE FUNCTION apply_allocation();

-- Issued documents are append-only except their status fields.
-- +goose StatementBegin
CREATE FUNCTION invoice_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'invoices cannot be deleted; void them instead' USING ERRCODE = 'PXIMM';
  END IF;
  IF OLD.status = 'void' THEN
    RAISE EXCEPTION 'a voided invoice cannot be changed' USING ERRCODE = 'PXIMM';
  END IF;
  IF (to_jsonb(NEW) - 'paid' - 'status' - 'void_entry_id' - 'notes' - 'updated_at')
     <> (to_jsonb(OLD) - 'paid' - 'status' - 'void_entry_id' - 'notes' - 'updated_at') THEN
    RAISE EXCEPTION 'issued invoices cannot be changed; void and re-issue instead' USING ERRCODE = 'PXIMM';
  END IF;
  IF NEW.status = 'void' AND OLD.paid <> 0 THEN
    RAISE EXCEPTION 'void the payments of this invoice first' USING ERRCODE = 'PXSTA';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER invoices_guard BEFORE UPDATE OR DELETE ON invoices FOR EACH ROW EXECUTE FUNCTION invoice_guard();

-- +goose StatementBegin
CREATE FUNCTION payment_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'payments cannot be deleted; void them instead' USING ERRCODE = 'PXIMM';
  END IF;
  IF OLD.status = 'void' THEN
    RAISE EXCEPTION 'a voided payment cannot be changed' USING ERRCODE = 'PXIMM';
  END IF;
  IF (to_jsonb(NEW) - 'status' - 'void_entry_id' - 'notes' - 'updated_at')
     <> (to_jsonb(OLD) - 'status' - 'void_entry_id' - 'notes' - 'updated_at') THEN
    RAISE EXCEPTION 'payments cannot be changed; void and re-enter instead' USING ERRCODE = 'PXIMM';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER payments_guard BEFORE UPDATE OR DELETE ON payments FOR EACH ROW EXECUTE FUNCTION payment_guard();
CREATE TRIGGER invoice_lines_immutable BEFORE UPDATE OR DELETE ON invoice_lines FOR EACH ROW EXECUTE FUNCTION forbid_change();
CREATE TRIGGER payment_tenders_immutable BEFORE UPDATE OR DELETE ON payment_tenders FOR EACH ROW EXECUTE FUNCTION forbid_change();
CREATE TRIGGER payment_allocations_immutable BEFORE UPDATE OR DELETE ON payment_allocations FOR EACH ROW EXECUTE FUNCTION forbid_change();

CREATE TRIGGER payment_methods_touch BEFORE UPDATE ON payment_methods FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER areas_touch BEFORE UPDATE ON areas FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER plans_touch BEFORE UPDATE ON plans FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER customers_touch BEFORE UPDATE ON customers FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER subscriptions_touch BEFORE UPDATE ON subscriptions FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER meter_readings_touch BEFORE UPDATE ON meter_readings FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER invoices_touch BEFORE UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER payments_touch BEFORE UPDATE ON payments FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

CREATE TRIGGER payment_methods_audit AFTER INSERT OR UPDATE OR DELETE ON payment_methods FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER areas_audit AFTER INSERT OR UPDATE OR DELETE ON areas FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER plans_audit AFTER INSERT OR UPDATE OR DELETE ON plans FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER customers_audit AFTER INSERT OR UPDATE OR DELETE ON customers FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER subscriptions_audit AFTER INSERT OR UPDATE OR DELETE ON subscriptions FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER meter_readings_audit AFTER INSERT OR UPDATE OR DELETE ON meter_readings FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER invoices_audit AFTER INSERT OR UPDATE OR DELETE ON invoices FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER payments_audit AFTER INSERT OR UPDATE OR DELETE ON payments FOR EACH ROW EXECUTE FUNCTION audit_row();

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['doc_counters', 'payment_methods', 'areas', 'plans', 'customers', 'subscriptions',
                           'meter_readings', 'invoices', 'invoice_lines', 'payments', 'payment_tenders',
                           'payment_allocations'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
  END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS payment_allocations, payment_tenders, payments, invoice_lines, invoices, meter_readings,
  subscriptions, customers, plans, areas, payment_methods, doc_counters;
DROP FUNCTION IF EXISTS apply_allocation(), invoice_guard(), payment_guard(), next_doc_number(uuid, text);
ALTER TABLE companies DROP COLUMN IF EXISTS ar_account_id, DROP COLUMN IF EXISTS fx_gain_account_id,
  DROP COLUMN IF EXISTS fx_loss_account_id, DROP COLUMN IF EXISTS default_revenue_account_id;
