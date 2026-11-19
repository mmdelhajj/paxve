-- Module U: HR & payroll (employee file, documents, schedules, attendance, leave, payroll rules per country,
-- payslip lines, split salaries in two currencies, approval step) + module T leftovers.

-- +goose Up

-- T leftover: a landed cost leaves a value-only movement (quantity 0) in the stock-movements report
ALTER TABLE stock_moves DROP CONSTRAINT IF EXISTS stock_moves_quantity_check;
ALTER TABLE stock_moves ADD CONSTRAINT stock_moves_quantity_check CHECK (quantity <> 0 OR source = 'landed_cost');

-- ---------- employee file ----------
ALTER TABLE employees
  ADD COLUMN email            text NOT NULL DEFAULT '',
  ADD COLUMN national_id      text NOT NULL DEFAULT '',
  ADD COLUMN nssf_number      text NOT NULL DEFAULT '',
  ADD COLUMN nationality      text NOT NULL DEFAULT '' CHECK (nationality ~ '^([A-Z]{2})?$'),
  ADD COLUMN birth_date       date,
  ADD COLUMN gender           text NOT NULL DEFAULT '' CHECK (gender IN ('', 'm', 'f')),
  ADD COLUMN marital_status   text NOT NULL DEFAULT '' CHECK (marital_status IN ('', 'single', 'married', 'divorced', 'widowed')),
  ADD COLUMN spouse_working   boolean NOT NULL DEFAULT false,
  ADD COLUMN children         int NOT NULL DEFAULT 0 CHECK (children BETWEEN 0 AND 20),
  ADD COLUMN address          text NOT NULL DEFAULT '',
  ADD COLUMN department       text NOT NULL DEFAULT '',
  ADD COLUMN manager_id       uuid,
  ADD COLUMN contract_type    text NOT NULL DEFAULT 'permanent' CHECK (contract_type IN ('permanent', 'fixed', 'part_time', 'daily', 'contractor')),
  ADD COLUMN contract_start   date,
  ADD COLUMN contract_end     date,
  ADD COLUMN end_date         date,
  ADD COLUMN bank_name        text NOT NULL DEFAULT '',
  ADD COLUMN bank_account     text NOT NULL DEFAULT '',
  ADD COLUMN bank_routing     text NOT NULL DEFAULT '',
  ADD COLUMN photo            text NOT NULL DEFAULT '' CHECK (length(photo) <= 400000),
  ADD COLUMN currency2        char(3) REFERENCES currencies (code),
  ADD COLUMN salary2          numeric(20, 4) NOT NULL DEFAULT 0 CHECK (salary2 >= 0),
  ADD COLUMN transport_daily  boolean NOT NULL DEFAULT false,
  ADD COLUMN housing          numeric(20, 4) NOT NULL DEFAULT 0 CHECK (housing >= 0),
  ADD COLUMN commission_pct   numeric(7, 4) NOT NULL DEFAULT 0 CHECK (commission_pct BETWEEN 0 AND 100),
  ADD COLUMN track_attendance boolean NOT NULL DEFAULT false,
  ADD COLUMN tax_exempt       boolean NOT NULL DEFAULT false,
  ADD COLUMN schedule_id      uuid,
  ADD FOREIGN KEY (tenant_id, manager_id) REFERENCES employees (tenant_id, id);
ALTER TABLE employees ADD CONSTRAINT employees_salary2_currency CHECK (salary2 = 0 OR currency2 IS NOT NULL);
CREATE UNIQUE INDEX employees_user_once ON employees (tenant_id, company_id, user_id) WHERE user_id IS NOT NULL;

CREATE TABLE employee_documents (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  employee_id uuid NOT NULL,
  name        text NOT NULL CHECK (length(trim(name)) > 0),
  kind        text NOT NULL DEFAULT 'other',
  mime        text NOT NULL DEFAULT 'application/octet-stream',
  size        int NOT NULL CHECK (size BETWEEN 1 AND 5242880),
  data        bytea NOT NULL,
  expires_on  date,
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, employee_id) REFERENCES employees (tenant_id, id) ON DELETE CASCADE,
  UNIQUE (tenant_id, id)
);
CREATE INDEX employee_documents_emp ON employee_documents (tenant_id, employee_id);

-- additional fixed monthly allowances / deductions per employee
CREATE TABLE employee_components (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  employee_id uuid NOT NULL,
  kind        text NOT NULL CHECK (kind IN ('allowance', 'deduction')),
  name        text NOT NULL CHECK (length(trim(name)) > 0),
  currency    char(3) NOT NULL REFERENCES currencies (code),
  amount      numeric(20, 4) NOT NULL CHECK (amount > 0),
  taxable     boolean NOT NULL DEFAULT true,
  social      boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, employee_id) REFERENCES employees (tenant_id, id) ON DELETE CASCADE,
  UNIQUE (tenant_id, id)
);

-- ---------- schedules, holidays, attendance ----------
CREATE TABLE work_schedules (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  name          text NOT NULL CHECK (length(trim(name)) > 0),
  start_time    time NOT NULL DEFAULT '08:00',
  end_time      time NOT NULL DEFAULT '16:00',
  break_minutes int NOT NULL DEFAULT 0 CHECK (break_minutes BETWEEN 0 AND 600),
  grace_minutes int NOT NULL DEFAULT 10 CHECK (grace_minutes BETWEEN 0 AND 240),
  work_days     int[] NOT NULL DEFAULT '{1,2,3,4,5,6}', -- ISO day of week, 1 = Monday
  created_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, id),
  CHECK (work_days <@ '{1,2,3,4,5,6,7}'::int[])
);
ALTER TABLE employees ADD FOREIGN KEY (tenant_id, schedule_id) REFERENCES work_schedules (tenant_id, id);

CREATE TABLE public_holidays (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  day        date NOT NULL,
  name       text NOT NULL DEFAULT '',
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, company_id, day)
);

CREATE TABLE attendance (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  employee_id      uuid NOT NULL,
  work_date        date NOT NULL,
  check_in         timestamptz,
  check_out        timestamptz,
  source           text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'phone', 'device')),
  in_lat           numeric(9, 6),
  in_lng           numeric(9, 6),
  out_lat          numeric(9, 6),
  out_lng          numeric(9, 6),
  worked_minutes   int NOT NULL DEFAULT 0,
  late_minutes     int NOT NULL DEFAULT 0,
  overtime_minutes int NOT NULL DEFAULT 0,
  notes            text NOT NULL DEFAULT '',
  updated_by       uuid DEFAULT app_user(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, employee_id) REFERENCES employees (tenant_id, id) ON DELETE CASCADE,
  UNIQUE (tenant_id, employee_id, work_date),
  CHECK (check_out IS NULL OR check_in IS NULL OR check_out >= check_in)
);
CREATE INDEX attendance_company_day ON attendance (tenant_id, company_id, work_date);

-- ---------- leave ----------
CREATE TABLE leave_types (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  code          text NOT NULL,
  name_en       text NOT NULL,
  name_ar       text NOT NULL DEFAULT '',
  name_fr       text NOT NULL DEFAULT '',
  paid          boolean NOT NULL DEFAULT true,
  days_per_year numeric(6, 2) NOT NULL DEFAULT 0 CHECK (days_per_year >= 0),
  accrual       text NOT NULL DEFAULT 'yearly' CHECK (accrual IN ('yearly', 'monthly', 'none')),
  max_carry     numeric(6, 2) NOT NULL DEFAULT 0 CHECK (max_carry >= 0),
  is_active     boolean NOT NULL DEFAULT true,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, code)
);

CREATE TABLE leave_requests (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  employee_id uuid NOT NULL,
  type_id     uuid NOT NULL,
  date_from   date NOT NULL,
  date_to     date NOT NULL,
  days        numeric(6, 2) NOT NULL CHECK (days > 0),
  status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
  reason      text NOT NULL DEFAULT '',
  decided_by  uuid,
  decided_at  timestamptz,
  decision_note text NOT NULL DEFAULT '',
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, employee_id) REFERENCES employees (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, type_id) REFERENCES leave_types (tenant_id, id),
  UNIQUE (tenant_id, id),
  CHECK (date_to >= date_from)
);
CREATE INDEX leave_requests_emp ON leave_requests (tenant_id, employee_id, date_from);

-- ---------- payroll rules (per company overrides of the country defaults, with effective dates) ----------
CREATE TABLE payroll_params (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  key            text NOT NULL CHECK (key ~ '^[a-z0-9_]+$'),
  value          text NOT NULL,
  effective_from date NOT NULL,
  created_by     uuid DEFAULT app_user(),
  created_at     timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, company_id, key, effective_from)
);

ALTER TABLE companies ADD COLUMN income_tax_payable_account_id uuid;
ALTER TABLE companies ADD FOREIGN KEY (tenant_id, income_tax_payable_account_id) REFERENCES accounts (tenant_id, id);
ALTER TABLE companies ADD COLUMN wps_employer_id text NOT NULL DEFAULT '';
ALTER TABLE companies ADD COLUMN wps_bank_code text NOT NULL DEFAULT '';

-- ---------- payroll: approval step, split salaries, detailed lines ----------
ALTER TABLE payroll_runs DROP CONSTRAINT IF EXISTS payroll_runs_status_check;
ALTER TABLE payroll_runs ADD CONSTRAINT payroll_runs_status_check CHECK (status IN ('draft', 'approved', 'posted', 'void'));
ALTER TABLE payroll_runs ADD COLUMN approved_by uuid, ADD COLUMN approved_at timestamptz, ADD COLUMN rate_note text NOT NULL DEFAULT '';

ALTER TABLE payslips
  ADD COLUMN currency2      char(3) REFERENCES currencies (code),
  ADD COLUMN base2          numeric(20, 4) NOT NULL DEFAULT 0,
  ADD COLUMN allowances2    numeric(20, 4) NOT NULL DEFAULT 0,
  ADD COLUMN deductions2    numeric(20, 4) NOT NULL DEFAULT 0,
  ADD COLUMN net2           numeric(20, 4) NOT NULL DEFAULT 0,
  ADD COLUMN paid2          numeric(20, 4) NOT NULL DEFAULT 0,
  ADD COLUMN worked_days    numeric(6, 2) NOT NULL DEFAULT 0,
  ADD COLUMN absent_days    numeric(6, 2) NOT NULL DEFAULT 0,
  ADD COLUMN unpaid_days    numeric(6, 2) NOT NULL DEFAULT 0,
  ADD COLUMN overtime_hours numeric(8, 2) NOT NULL DEFAULT 0,
  ADD COLUMN detailed       boolean NOT NULL DEFAULT false,
  ADD COLUMN manual_allow   numeric(20, 4) NOT NULL DEFAULT 0 CHECK (manual_allow >= 0),
  ADD COLUMN manual_ded     numeric(20, 4) NOT NULL DEFAULT 0 CHECK (manual_ded >= 0),
  ADD COLUMN manual_recovery numeric(20, 4) CHECK (manual_recovery >= 0);
ALTER TABLE employees ADD COLUMN device_id text NOT NULL DEFAULT '';
ALTER TABLE payslips ADD CONSTRAINT payslips_net2 CHECK (net2 = base2 + allowances2 - deductions2 AND net2 >= 0 AND paid2 >= 0 AND paid2 <= net2);

CREATE TABLE payslip_lines (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  payslip_id uuid NOT NULL,
  seq        int NOT NULL,
  code       text NOT NULL,
  label      text NOT NULL,
  kind       text NOT NULL CHECK (kind IN ('earning', 'deduction', 'employer', 'info')),
  -- posting role: salary | allowance_social (family allowance paid for the social fund) | social | tax | advance | other
  role       text NOT NULL DEFAULT 'salary',
  currency   char(3) NOT NULL REFERENCES currencies (code),
  amount     numeric(20, 4) NOT NULL,
  basis      text NOT NULL DEFAULT '',
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, payslip_id) REFERENCES payslips (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX payslip_lines_slip ON payslip_lines (tenant_id, payslip_id, seq);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION payslip_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE v_status text;
BEGIN
  SELECT status INTO v_status FROM payroll_runs WHERE id = coalesce(NEW.run_id, OLD.run_id);
  IF v_status IS NULL OR v_status = 'draft' THEN -- NULL: the run itself is being discarded (cascade)
    RETURN coalesce(NEW, OLD);
  END IF;
  IF TG_OP = 'UPDATE' AND v_status = 'posted'
     AND (to_jsonb(NEW) - 'paid' - 'paid2' - 'updated_at') = (to_jsonb(OLD) - 'paid' - 'paid2' - 'updated_at') THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'payslips of an approved or posted payroll cannot be changed' USING ERRCODE = 'PXIMM';
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION payslip_line_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE v_status text;
BEGIN
  SELECT r.status INTO v_status FROM payslips p JOIN payroll_runs r ON r.id = p.run_id WHERE p.id = coalesce(NEW.payslip_id, OLD.payslip_id);
  IF v_status IS NULL OR v_status = 'draft' THEN
    RETURN coalesce(NEW, OLD);
  END IF;
  RAISE EXCEPTION 'payslips of an approved or posted payroll cannot be changed' USING ERRCODE = 'PXIMM';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER payslip_lines_guard BEFORE INSERT OR UPDATE OR DELETE ON payslip_lines FOR EACH ROW EXECUTE FUNCTION payslip_line_guard();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION payroll_run_guard() RETURNS trigger
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
  IF OLD.status = 'approved' AND NEW.status NOT IN ('approved', 'draft', 'posted') THEN
    RAISE EXCEPTION 'an approved payroll can only go back to draft or be posted' USING ERRCODE = 'PXIMM';
  END IF;
  IF OLD.status = 'posted' AND (NEW.status <> 'void'
     OR (to_jsonb(NEW) - 'status' - 'void_entry_id' - 'updated_at') <> (to_jsonb(OLD) - 'status' - 'void_entry_id' - 'updated_at')) THEN
    RAISE EXCEPTION 'a posted payroll cannot be changed' USING ERRCODE = 'PXIMM';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd

-- company scope (module Q): company tables get the restrictive company policy and FORCE RLS like the others
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['employee_documents', 'employee_components', 'work_schedules', 'public_holidays', 'attendance',
                           'leave_types', 'leave_requests', 'payroll_params', 'payslip_lines'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
    -- audit trail (documents carry file bytes and payslip lines are derived: not audited row by row)
    IF t NOT IN ('employee_documents', 'payslip_lines') THEN
      EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    END IF;
  END LOOP;
END
$$;
-- +goose StatementEnd
GRANT DELETE ON employee_documents, employee_components, public_holidays, payroll_params, payslip_lines TO paxve_app;

-- +goose Down
SELECT 1;
