-- Back office: HR & payroll, employee advances, fixed assets & depreciation, recurring entries,
-- bank statements & reconciliation.
-- Rules enforced here:
--   * a posted payroll run, its payslips, depreciation lines and asset disposals are never changed or deleted
--   * an asset is depreciated at most once per month (UNIQUE asset_id + period)
--   * a journal line can be matched to at most one bank statement line
--   * advances can never be recovered more than lent; payslips never paid more than their net

-- +goose Up

-- Default accounts and NSSF rates (rates are configurable; defaults follow common Lebanese practice:
-- employee 3%, employer 21.5% — verify with the current NSSF circulars; ceilings are not modelled).
ALTER TABLE companies
  ADD COLUMN salary_expense_account_id uuid,
  ADD COLUMN nssf_expense_account_id uuid,
  ADD COLUMN salary_payable_account_id uuid,
  ADD COLUMN nssf_payable_account_id uuid,
  ADD COLUMN advances_account_id uuid,
  ADD COLUMN depreciation_expense_account_id uuid,
  ADD COLUMN accum_depreciation_account_id uuid,
  ADD COLUMN asset_gain_account_id uuid,
  ADD COLUMN asset_loss_account_id uuid,
  ADD COLUMN nssf_employee_rate numeric(6, 3) NOT NULL DEFAULT 3 CHECK (nssf_employee_rate BETWEEN 0 AND 100),
  ADD COLUMN nssf_employer_rate numeric(6, 3) NOT NULL DEFAULT 21.5 CHECK (nssf_employer_rate BETWEEN 0 AND 100),
  ADD FOREIGN KEY (tenant_id, salary_expense_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, nssf_expense_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, salary_payable_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, nssf_payable_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, advances_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, depreciation_expense_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, accum_depreciation_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, asset_gain_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, asset_loss_account_id) REFERENCES accounts (tenant_id, id);

UPDATE companies c SET
  salary_expense_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('641', '6200') ORDER BY a.code LIMIT 1),
  nssf_expense_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('645', '6200') ORDER BY a.code LIMIT 1),
  salary_payable_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('421', '2300') ORDER BY a.code LIMIT 1),
  nssf_payable_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('431', '2200') ORDER BY a.code LIMIT 1),
  advances_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('421', '2300') ORDER BY a.code LIMIT 1),
  depreciation_expense_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('68', '6600') ORDER BY a.code LIMIT 1),
  accum_depreciation_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('28', '1750') ORDER BY a.code LIMIT 1),
  asset_gain_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('77', '4900') ORDER BY a.code LIMIT 1),
  asset_loss_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('68', '6900') ORDER BY a.code LIMIT 1);

-- ---------- HR ----------

CREATE TABLE employees (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  code           text NOT NULL,
  name           text NOT NULL CHECK (length(trim(name)) > 0),
  phone          text NOT NULL DEFAULT '',
  position       text NOT NULL DEFAULT '',
  hire_date      date NOT NULL DEFAULT current_date,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  currency       char(3) NOT NULL REFERENCES currencies (code),
  salary         numeric(20, 4) NOT NULL DEFAULT 0 CHECK (salary >= 0),
  transport      numeric(20, 4) NOT NULL DEFAULT 0 CHECK (transport >= 0),
  nssf_enrolled  boolean NOT NULL DEFAULT true,
  method_id      uuid,
  user_id        uuid,
  notes          text NOT NULL DEFAULT '',
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, method_id) REFERENCES payment_methods (tenant_id, id),
  FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, code)
);

CREATE TABLE employee_advances (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  employee_id      uuid NOT NULL,
  advance_date     date NOT NULL,
  currency         char(3) NOT NULL REFERENCES currencies (code),
  amount           numeric(20, 4) NOT NULL CHECK (amount > 0),
  installment      numeric(20, 4) NOT NULL CHECK (installment > 0),
  recovered        numeric(20, 4) NOT NULL DEFAULT 0,
  method_id        uuid NOT NULL,
  journal_entry_id uuid NOT NULL,
  notes            text NOT NULL DEFAULT '',
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, employee_id) REFERENCES employees (tenant_id, id),
  FOREIGN KEY (tenant_id, method_id) REFERENCES payment_methods (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (tenant_id, id),
  CHECK (recovered >= 0 AND recovered <= amount)
);

CREATE TABLE payroll_runs (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  period           text NOT NULL CHECK (period ~ '^\d{4}-(0[1-9]|1[0-2])$'),
  status           text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'posted', 'void')),
  posting_date     date,
  journal_entry_id uuid,
  void_entry_id    uuid,
  nssf_employee_rate numeric(6, 3) NOT NULL,
  nssf_employer_rate numeric(6, 3) NOT NULL,
  created_by       uuid DEFAULT app_user(),
  posted_by        uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  FOREIGN KEY (tenant_id, void_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (tenant_id, id)
);
CREATE UNIQUE INDEX payroll_runs_one_per_period ON payroll_runs (tenant_id, company_id, period) WHERE status <> 'void';

CREATE TABLE payslips (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  run_id           uuid NOT NULL,
  employee_id      uuid NOT NULL,
  currency         char(3) NOT NULL REFERENCES currencies (code),
  base             numeric(20, 4) NOT NULL DEFAULT 0 CHECK (base >= 0),
  transport        numeric(20, 4) NOT NULL DEFAULT 0 CHECK (transport >= 0),
  allowances       numeric(20, 4) NOT NULL DEFAULT 0 CHECK (allowances >= 0),
  deductions       numeric(20, 4) NOT NULL DEFAULT 0 CHECK (deductions >= 0),
  nssf_employee    numeric(20, 4) NOT NULL DEFAULT 0 CHECK (nssf_employee >= 0),
  nssf_employer    numeric(20, 4) NOT NULL DEFAULT 0 CHECK (nssf_employer >= 0),
  advance_recovery numeric(20, 4) NOT NULL DEFAULT 0 CHECK (advance_recovery >= 0),
  net              numeric(20, 4) NOT NULL DEFAULT 0 CHECK (net >= 0),
  paid             numeric(20, 4) NOT NULL DEFAULT 0,
  notes            text NOT NULL DEFAULT '',
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, run_id) REFERENCES payroll_runs (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, employee_id) REFERENCES employees (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, run_id, employee_id),
  CHECK (paid >= 0 AND paid <= net),
  CHECK (net = base + transport + allowances - deductions - nssf_employee - advance_recovery)
);

CREATE TABLE payroll_payments (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  run_id           uuid NOT NULL,
  payment_date     date NOT NULL,
  method_id        uuid NOT NULL,
  currency         char(3) NOT NULL REFERENCES currencies (code),
  amount           numeric(20, 4) NOT NULL CHECK (amount > 0),
  journal_entry_id uuid NOT NULL,
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES payroll_runs (tenant_id, id),
  FOREIGN KEY (tenant_id, method_id) REFERENCES payment_methods (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id)
);

-- Payslips may be edited only while their run is a draft (paid changes after posting through payroll_payments).
-- +goose StatementBegin
CREATE FUNCTION payslip_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE v_status text;
BEGIN
  SELECT status INTO v_status FROM payroll_runs WHERE id = coalesce(NEW.run_id, OLD.run_id);
  IF v_status IS NULL OR v_status = 'draft' THEN -- NULL: the run itself is being discarded (cascade)
    RETURN coalesce(NEW, OLD);
  END IF;
  IF TG_OP = 'UPDATE' AND v_status = 'posted'
     AND (to_jsonb(NEW) - 'paid' - 'updated_at') = (to_jsonb(OLD) - 'paid' - 'updated_at') THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'payslips of a posted payroll cannot be changed' USING ERRCODE = 'PXIMM';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER payslips_guard BEFORE INSERT OR UPDATE OR DELETE ON payslips FOR EACH ROW EXECUTE FUNCTION payslip_guard();

-- +goose StatementBegin
CREATE FUNCTION payroll_run_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.status <> 'draft' THEN
      RAISE EXCEPTION 'a posted payroll cannot be deleted; void it instead' USING ERRCODE = 'PXIMM';
    END IF;
    RETURN OLD;
  END IF;
  IF OLD.status = 'void' THEN
    RAISE EXCEPTION 'a voided payroll cannot be changed' USING ERRCODE = 'PXIMM';
  END IF;
  IF OLD.status = 'posted' AND (NEW.status <> 'void'
     OR (to_jsonb(NEW) - 'status' - 'void_entry_id' - 'updated_at') <> (to_jsonb(OLD) - 'status' - 'void_entry_id' - 'updated_at')) THEN
    RAISE EXCEPTION 'a posted payroll cannot be changed' USING ERRCODE = 'PXIMM';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER payroll_runs_guard BEFORE UPDATE OR DELETE ON payroll_runs FOR EACH ROW EXECUTE FUNCTION payroll_run_guard();
CREATE TRIGGER payroll_payments_immutable BEFORE UPDATE OR DELETE ON payroll_payments FOR EACH ROW EXECUTE FUNCTION forbid_change();

-- ---------- fixed assets ----------

CREATE TABLE assets (
  id                   uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id            uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id           uuid NOT NULL,
  code                 text NOT NULL,
  name                 text NOT NULL CHECK (length(trim(name)) > 0),
  category             text NOT NULL DEFAULT '',
  purchase_date        date NOT NULL,
  currency             char(3) NOT NULL REFERENCES currencies (code),
  cost                 numeric(20, 4) NOT NULL CHECK (cost > 0),
  cost_base            numeric(20, 4) NOT NULL CHECK (cost_base > 0),
  salvage_base         numeric(20, 4) NOT NULL DEFAULT 0 CHECK (salvage_base >= 0),
  useful_life_months   int NOT NULL CHECK (useful_life_months BETWEEN 1 AND 1200),
  start_period         text NOT NULL CHECK (start_period ~ '^\d{4}-(0[1-9]|1[0-2])$'),
  asset_account_id     uuid NOT NULL,
  accum_account_id     uuid NOT NULL,
  expense_account_id   uuid NOT NULL,
  status               text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disposed')),
  purchase_entry_id    uuid,
  disposal_date        date,
  disposal_proceeds_base numeric(20, 4),
  disposal_entry_id    uuid,
  notes                text NOT NULL DEFAULT '',
  created_by           uuid DEFAULT app_user(),
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, asset_account_id) REFERENCES accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, accum_account_id) REFERENCES accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, expense_account_id) REFERENCES accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, purchase_entry_id) REFERENCES journal_entries (tenant_id, id),
  FOREIGN KEY (tenant_id, disposal_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, code),
  CHECK (salvage_base < cost_base)
);

CREATE TABLE depreciation_lines (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  asset_id         uuid NOT NULL,
  period           text NOT NULL CHECK (period ~ '^\d{4}-(0[1-9]|1[0-2])$'),
  amount           numeric(20, 4) NOT NULL CHECK (amount > 0),
  journal_entry_id uuid NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES assets (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (tenant_id, asset_id, period)
);
CREATE TRIGGER depreciation_lines_immutable BEFORE UPDATE OR DELETE ON depreciation_lines FOR EACH ROW EXECUTE FUNCTION forbid_change();

-- +goose StatementBegin
CREATE FUNCTION asset_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'assets cannot be deleted; dispose them instead' USING ERRCODE = 'PXIMM';
  END IF;
  IF OLD.status = 'disposed' THEN
    RAISE EXCEPTION 'a disposed asset cannot be changed' USING ERRCODE = 'PXIMM';
  END IF;
  -- money and accounts are frozen once depreciation has started
  IF EXISTS (SELECT 1 FROM depreciation_lines WHERE asset_id = OLD.id)
     AND (NEW.cost_base, NEW.salvage_base, NEW.useful_life_months, NEW.start_period, NEW.asset_account_id,
          NEW.accum_account_id, NEW.expense_account_id, NEW.currency, NEW.cost)
      IS DISTINCT FROM (OLD.cost_base, OLD.salvage_base, OLD.useful_life_months, OLD.start_period, OLD.asset_account_id,
          OLD.accum_account_id, OLD.expense_account_id, OLD.currency, OLD.cost) THEN
    RAISE EXCEPTION 'depreciation has started: cost, life and accounts can no longer change' USING ERRCODE = 'PXIMM';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER assets_guard BEFORE UPDATE OR DELETE ON assets FOR EACH ROW EXECUTE FUNCTION asset_guard();

-- ---------- recurring entries ----------

CREATE TABLE recurring_templates (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  name        text NOT NULL CHECK (length(trim(name)) > 0),
  kind        text NOT NULL CHECK (kind IN ('journal', 'expense')),
  frequency   text NOT NULL CHECK (frequency IN ('monthly', 'quarterly', 'yearly')),
  next_date   date NOT NULL,
  end_date    date,
  is_active   boolean NOT NULL DEFAULT true,
  currency    char(3) NOT NULL REFERENCES currencies (code),
  amount      numeric(20, 4),                  -- expense
  account_id  uuid,                            -- expense: expense account
  method_id   uuid,                            -- expense: paid from
  lines       jsonb NOT NULL DEFAULT '[]'::jsonb, -- journal: [{account_id, description, debit, credit}]
  memo        text NOT NULL DEFAULT '',
  runs        int NOT NULL DEFAULT 0,
  last_run_at timestamptz,
  last_entry_id uuid,
  last_error  text NOT NULL DEFAULT '',
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, method_id) REFERENCES payment_methods (tenant_id, id),
  UNIQUE (tenant_id, id),
  CHECK (jsonb_typeof(lines) = 'array'),
  CHECK (kind <> 'expense' OR (amount > 0 AND account_id IS NOT NULL AND method_id IS NOT NULL))
);
CREATE INDEX recurring_due_idx ON recurring_templates (next_date) WHERE is_active;

-- ---------- bank reconciliation ----------

CREATE TABLE bank_statements (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  method_id   uuid NOT NULL,
  name        text NOT NULL DEFAULT '',
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, method_id) REFERENCES payment_methods (tenant_id, id),
  UNIQUE (tenant_id, id)
);

CREATE TABLE bank_statement_lines (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  statement_id     uuid NOT NULL,
  method_id        uuid NOT NULL,
  line_date        date NOT NULL,
  description      text NOT NULL DEFAULT '',
  reference        text NOT NULL DEFAULT '',
  amount           numeric(20, 4) NOT NULL CHECK (amount <> 0), -- + money in, - money out
  journal_line_id  uuid,
  status           text NOT NULL DEFAULT 'unmatched' CHECK (status IN ('unmatched', 'matched', 'ignored')),
  created_entry_id uuid,
  matched_by       uuid,
  matched_at       timestamptz,
  FOREIGN KEY (tenant_id, statement_id) REFERENCES bank_statements (tenant_id, id),
  FOREIGN KEY (tenant_id, method_id) REFERENCES payment_methods (tenant_id, id),
  FOREIGN KEY (tenant_id, created_entry_id) REFERENCES journal_entries (tenant_id, id),
  CHECK ((status = 'matched') = (journal_line_id IS NOT NULL))
);
CREATE UNIQUE INDEX bank_lines_one_match ON bank_statement_lines (tenant_id, journal_line_id) WHERE journal_line_id IS NOT NULL;
CREATE INDEX bank_lines_method_idx ON bank_statement_lines (tenant_id, method_id, line_date);

-- ---------- common ----------

CREATE TRIGGER employees_touch BEFORE UPDATE ON employees FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER employee_advances_touch BEFORE UPDATE ON employee_advances FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER payroll_runs_touch BEFORE UPDATE ON payroll_runs FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER payslips_touch BEFORE UPDATE ON payslips FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER assets_touch BEFORE UPDATE ON assets FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER recurring_templates_touch BEFORE UPDATE ON recurring_templates FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

CREATE TRIGGER employees_audit AFTER INSERT OR UPDATE OR DELETE ON employees FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER employee_advances_audit AFTER INSERT OR UPDATE OR DELETE ON employee_advances FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER payroll_runs_audit AFTER INSERT OR UPDATE OR DELETE ON payroll_runs FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER payslips_audit AFTER INSERT OR UPDATE OR DELETE ON payslips FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER payroll_payments_audit AFTER INSERT ON payroll_payments FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER assets_audit AFTER INSERT OR UPDATE OR DELETE ON assets FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER depreciation_lines_audit AFTER INSERT ON depreciation_lines FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER recurring_templates_audit AFTER INSERT OR UPDATE OR DELETE ON recurring_templates FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER bank_statements_audit AFTER INSERT ON bank_statements FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER bank_statement_lines_audit AFTER INSERT OR UPDATE ON bank_statement_lines FOR EACH ROW EXECUTE FUNCTION audit_row();

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['employees', 'employee_advances', 'payroll_runs', 'payslips', 'payroll_payments', 'assets',
                           'depreciation_lines', 'recurring_templates', 'bank_statements', 'bank_statement_lines'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
  END LOOP;
END
$$;
-- +goose StatementEnd
-- draft payroll runs (and their payslips, by cascade) may be discarded
GRANT DELETE ON payroll_runs, payslips TO paxve_app;

-- +goose Down
DROP TABLE IF EXISTS bank_statement_lines, bank_statements, recurring_templates, depreciation_lines, assets,
  payroll_payments, payslips, payroll_runs, employee_advances, employees;
DROP FUNCTION IF EXISTS payslip_guard(), payroll_run_guard(), asset_guard();
ALTER TABLE companies DROP COLUMN IF EXISTS salary_expense_account_id, DROP COLUMN IF EXISTS nssf_expense_account_id,
  DROP COLUMN IF EXISTS salary_payable_account_id, DROP COLUMN IF EXISTS nssf_payable_account_id,
  DROP COLUMN IF EXISTS advances_account_id, DROP COLUMN IF EXISTS depreciation_expense_account_id,
  DROP COLUMN IF EXISTS accum_depreciation_account_id, DROP COLUMN IF EXISTS asset_gain_account_id,
  DROP COLUMN IF EXISTS asset_loss_account_id, DROP COLUMN IF EXISTS nssf_employee_rate, DROP COLUMN IF EXISTS nssf_employer_rate;
