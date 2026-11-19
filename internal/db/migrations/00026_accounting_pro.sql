-- Module S: professional accounting.
--   * cost centers (branch / station / generator / project) tagged on journal lines; the tag of a posted line
--     may be corrected later (it never changes an amount), everything else stays immutable
--   * budgets per account / cost center / month
--   * cheques received and issued, with every step of their life (deposit, collect, endorse, bounce…)
--   * currency exchange vouchers (buy / sell dollars between cash boxes)
--   * fiscal years: closing entry (result → retained earnings) + lock, reopen
--   * pl_totals(): profit & loss totals without the year-closing entries

-- +goose Up

-- ---------------- cost centers ----------------
CREATE TABLE cost_centers (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  code         text NOT NULL CHECK (code ~ '^[0-9A-Za-z._\-]{1,20}$'),
  name         text NOT NULL CHECK (length(trim(name)) > 0),
  name_i18n    jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(name_i18n) = 'object'),
  kind         text NOT NULL DEFAULT 'other' CHECK (kind IN ('branch', 'station', 'generator', 'project', 'department', 'other')),
  station_id   uuid,
  generator_id uuid,
  is_active    boolean NOT NULL DEFAULT true,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT cost_centers_station_id_same_company FOREIGN KEY (company_id, station_id) REFERENCES stations (company_id, id),
  CONSTRAINT cost_centers_generator_id_same_company FOREIGN KEY (company_id, generator_id) REFERENCES generators (company_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, code)
);
CREATE TRIGGER cost_centers_touch BEFORE UPDATE ON cost_centers FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

ALTER TABLE journal_lines ADD COLUMN cost_center_id uuid;
ALTER TABLE journal_lines ADD CONSTRAINT journal_lines_cost_center_id_same_company
  FOREIGN KEY (company_id, cost_center_id) REFERENCES cost_centers (company_id, id);
CREATE INDEX journal_lines_cost_center_idx ON journal_lines (company_id, cost_center_id) WHERE cost_center_id IS NOT NULL;

-- lines of a posted entry are immutable, except their cost center tag (analytic, never an amount)
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION journal_line_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_status text;
BEGIN
  IF TG_OP = 'UPDATE' AND (to_jsonb(NEW) - 'cost_center_id') = (to_jsonb(OLD) - 'cost_center_id') THEN
    RETURN NEW;
  END IF;
  IF TG_OP IN ('UPDATE', 'DELETE') THEN
    SELECT status INTO v_status FROM journal_entries WHERE id = OLD.entry_id;
    IF v_status = 'posted' THEN
      RAISE EXCEPTION 'lines of a posted journal entry cannot be changed' USING ERRCODE = 'PXIMM';
    END IF;
  END IF;
  IF TG_OP IN ('INSERT', 'UPDATE') THEN
    SELECT status INTO v_status FROM journal_entries WHERE id = NEW.entry_id;
    IF v_status = 'posted' THEN
      RAISE EXCEPTION 'lines cannot be added to a posted journal entry' USING ERRCODE = 'PXIMM';
    END IF;
  END IF;
  RETURN coalesce(NEW, OLD);
END
$$;
-- +goose StatementEnd

-- reversals keep the cost center of the reversed lines
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reverse_journal_entry(p_id uuid, p_date date, p_memo text) RETURNS uuid
LANGUAGE plpgsql AS $$
DECLARE
  v_orig journal_entries%ROWTYPE;
  v_new  uuid;
  v_lock date;
  v_date date;
BEGIN
  SELECT * INTO v_orig FROM journal_entries WHERE id = p_id FOR UPDATE;
  IF NOT FOUND OR v_orig.status <> 'posted' THEN
    RAISE EXCEPTION 'only posted entries can be reversed' USING ERRCODE = 'PXSTA';
  END IF;
  IF v_orig.reversed_by IS NOT NULL OR v_orig.reversal_of IS NOT NULL THEN
    RAISE EXCEPTION 'this entry is already reversed or is itself a reversal' USING ERRCODE = 'PXSTA';
  END IF;

  v_lock := company_lock_date(v_orig.company_id);
  v_date := coalesce(p_date, v_orig.entry_date);
  IF p_date IS NULL AND v_lock IS NOT NULL AND v_date <= v_lock THEN
    v_date := v_lock + 1;
  END IF;

  INSERT INTO journal_entries (company_id, entry_date, memo, reference, source, reversal_of)
  VALUES (v_orig.company_id, v_date,
          coalesce(nullif(p_memo, ''), 'Reversal of #' || v_orig.number), v_orig.reference, 'reversal', p_id)
  RETURNING id INTO v_new;

  INSERT INTO journal_lines (entry_id, line_no, account_id, description, currency, debit, credit, rate, cost_center_id)
  SELECT v_new, line_no, account_id, description, currency, credit, debit, rate, cost_center_id
  FROM journal_lines WHERE entry_id = p_id;

  PERFORM post_journal_entry(v_new);
  UPDATE journal_entries SET reversed_by = v_new WHERE id = p_id;
  RETURN v_new;
END
$$;
-- +goose StatementEnd

-- ---------------- budgets ----------------
CREATE TABLE budget_lines (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  year           int NOT NULL CHECK (year BETWEEN 2000 AND 2200),
  month          int NOT NULL CHECK (month BETWEEN 1 AND 12),
  account_id     uuid NOT NULL,
  cost_center_id uuid,
  amount         numeric(20, 4) NOT NULL CHECK (amount >= 0),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT budget_lines_account_id_same_company FOREIGN KEY (company_id, account_id) REFERENCES accounts (company_id, id),
  CONSTRAINT budget_lines_cost_center_id_same_company FOREIGN KEY (company_id, cost_center_id) REFERENCES cost_centers (company_id, id),
  UNIQUE NULLS NOT DISTINCT (company_id, year, month, account_id, cost_center_id)
);

-- ---------------- company accounts used by cheques and year closing ----------------
ALTER TABLE companies
  ADD COLUMN cheque_in_hand_account_id uuid,
  ADD COLUMN cheque_collection_account_id uuid,
  ADD COLUMN cheque_payable_account_id uuid,
  ADD COLUMN retained_earnings_account_id uuid,
  ADD CONSTRAINT companies_cheque_in_hand_account_id_same_company FOREIGN KEY (id, cheque_in_hand_account_id) REFERENCES accounts (company_id, id),
  ADD CONSTRAINT companies_cheque_collection_account_id_same_company FOREIGN KEY (id, cheque_collection_account_id) REFERENCES accounts (company_id, id),
  ADD CONSTRAINT companies_cheque_payable_account_id_same_company FOREIGN KEY (id, cheque_payable_account_id) REFERENCES accounts (company_id, id),
  ADD CONSTRAINT companies_retained_earnings_account_id_same_company FOREIGN KEY (id, retained_earnings_account_id) REFERENCES accounts (company_id, id);

-- cheque "boxes": the payment method through which a cheque pays a customer / supplier document (never a normal tender)
ALTER TABLE payment_methods DROP CONSTRAINT payment_methods_kind_check;
ALTER TABLE payment_methods ADD CONSTRAINT payment_methods_kind_check CHECK (kind IN ('cash', 'wallet', 'bank', 'card', 'cheque'));

-- ---------------- cheques ----------------
CREATE TABLE cheques (
  id                   uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id            uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id           uuid NOT NULL,
  number               bigint NOT NULL,
  direction            text NOT NULL CHECK (direction IN ('in', 'out')),
  cheque_no            text NOT NULL DEFAULT '',
  bank_name            text NOT NULL DEFAULT '',
  drawer               text NOT NULL DEFAULT '',
  receive_date         date NOT NULL,
  due_date             date NOT NULL,
  currency             char(3) NOT NULL REFERENCES currencies (code),
  amount               numeric(20, 4) NOT NULL CHECK (amount > 0),
  remaining            numeric(20, 4) NOT NULL CHECK (remaining >= 0),
  customer_id          uuid,
  supplier_id          uuid,
  account_id           uuid,
  status               text NOT NULL CHECK (status IN ('in_hand', 'deposited', 'collected', 'endorsed', 'bounced', 'returned',
                                                       'settled', 'issued', 'paid', 'cancelled')),
  frozen               boolean NOT NULL DEFAULT false,
  bank_method_id       uuid,
  endorsed_supplier_id uuid,
  payment_id           uuid,
  supplier_payment_id  uuid,
  journal_entry_id     uuid,
  notes                text NOT NULL DEFAULT '',
  created_by           uuid DEFAULT app_user(),
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT cheques_customer_id_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  CONSTRAINT cheques_supplier_id_same_company FOREIGN KEY (company_id, supplier_id) REFERENCES suppliers (company_id, id),
  CONSTRAINT cheques_endorsed_supplier_id_same_company FOREIGN KEY (company_id, endorsed_supplier_id) REFERENCES suppliers (company_id, id),
  CONSTRAINT cheques_account_id_same_company FOREIGN KEY (company_id, account_id) REFERENCES accounts (company_id, id),
  CONSTRAINT cheques_bank_method_id_same_company FOREIGN KEY (company_id, bank_method_id) REFERENCES payment_methods (company_id, id),
  CONSTRAINT cheques_payment_id_same_company FOREIGN KEY (company_id, payment_id) REFERENCES payments (company_id, id),
  CONSTRAINT cheques_supplier_payment_id_same_company FOREIGN KEY (company_id, supplier_payment_id) REFERENCES supplier_payments (company_id, id),
  CONSTRAINT cheques_journal_entry_id_same_company FOREIGN KEY (company_id, journal_entry_id) REFERENCES journal_entries (company_id, id),
  CHECK (remaining <= amount),
  UNIQUE (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
CREATE INDEX cheques_due_idx ON cheques (company_id, status, due_date);
CREATE TRIGGER cheques_touch BEFORE UPDATE ON cheques FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

CREATE TABLE cheque_events (
  id                  uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id           uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id          uuid NOT NULL,
  cheque_id           uuid NOT NULL,
  kind                text NOT NULL,
  event_date          date NOT NULL,
  amount              numeric(20, 4) NOT NULL DEFAULT 0,
  status_before       text NOT NULL,
  status_after        text NOT NULL,
  method_id           uuid,
  supplier_id         uuid,
  journal_entry_id    uuid,
  payment_id          uuid,
  supplier_payment_id uuid,
  invoice_id          uuid,
  undone              boolean NOT NULL DEFAULT false,
  undo_entry_id       uuid,
  notes               text NOT NULL DEFAULT '',
  created_by          uuid DEFAULT app_user(),
  created_at          timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT cheque_events_cheque_id_same_company FOREIGN KEY (company_id, cheque_id) REFERENCES cheques (company_id, id),
  CONSTRAINT cheque_events_method_id_same_company FOREIGN KEY (company_id, method_id) REFERENCES payment_methods (company_id, id),
  CONSTRAINT cheque_events_supplier_id_same_company FOREIGN KEY (company_id, supplier_id) REFERENCES suppliers (company_id, id),
  CONSTRAINT cheque_events_journal_entry_id_same_company FOREIGN KEY (company_id, journal_entry_id) REFERENCES journal_entries (company_id, id),
  CONSTRAINT cheque_events_undo_entry_id_same_company FOREIGN KEY (company_id, undo_entry_id) REFERENCES journal_entries (company_id, id),
  CONSTRAINT cheque_events_payment_id_same_company FOREIGN KEY (company_id, payment_id) REFERENCES payments (company_id, id),
  CONSTRAINT cheque_events_supplier_payment_id_same_company FOREIGN KEY (company_id, supplier_payment_id) REFERENCES supplier_payments (company_id, id),
  CONSTRAINT cheque_events_invoice_id_same_company FOREIGN KEY (company_id, invoice_id) REFERENCES invoices (company_id, id),
  UNIQUE (tenant_id, id)
);
CREATE INDEX cheque_events_cheque_idx ON cheque_events (cheque_id, created_at);

-- ---------------- currency exchange vouchers ----------------
CREATE TABLE currency_exchanges (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  exchange_date    date NOT NULL,
  from_method_id   uuid NOT NULL,
  to_method_id     uuid NOT NULL,
  from_currency    char(3) NOT NULL REFERENCES currencies (code),
  from_amount      numeric(20, 4) NOT NULL CHECK (from_amount > 0),
  to_currency      char(3) NOT NULL REFERENCES currencies (code),
  to_amount        numeric(20, 4) NOT NULL CHECK (to_amount > 0),
  rate             numeric(28, 10) NOT NULL CHECK (rate > 0),
  counterparty     text NOT NULL DEFAULT '',
  status           text NOT NULL DEFAULT 'posted' CHECK (status IN ('posted', 'void')),
  journal_entry_id uuid,
  void_entry_id    uuid,
  notes            text NOT NULL DEFAULT '',
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT currency_exchanges_from_method_id_same_company FOREIGN KEY (company_id, from_method_id) REFERENCES payment_methods (company_id, id),
  CONSTRAINT currency_exchanges_to_method_id_same_company FOREIGN KEY (company_id, to_method_id) REFERENCES payment_methods (company_id, id),
  CONSTRAINT currency_exchanges_journal_entry_id_same_company FOREIGN KEY (company_id, journal_entry_id) REFERENCES journal_entries (company_id, id),
  CONSTRAINT currency_exchanges_void_entry_id_same_company FOREIGN KEY (company_id, void_entry_id) REFERENCES journal_entries (company_id, id),
  CHECK (from_currency <> to_currency),
  UNIQUE (tenant_id, id),
  UNIQUE (company_id, number)
);

-- ---------------- fiscal years ----------------
CREATE TABLE fiscal_years (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  year             int NOT NULL CHECK (year BETWEEN 2000 AND 2200),
  start_date       date NOT NULL,
  end_date         date NOT NULL,
  status           text NOT NULL CHECK (status IN ('closed', 'reopened')),
  result           numeric(20, 4) NOT NULL DEFAULT 0,
  closing_entry_id uuid,
  previous_lock    date,
  closed_by        uuid,
  closed_at        timestamptz,
  reopened_by      uuid,
  reopened_at      timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT fiscal_years_closing_entry_id_same_company FOREIGN KEY (company_id, closing_entry_id) REFERENCES journal_entries (company_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (company_id, year)
);

-- ---------------- security: tenant + company scope, audit, grants ----------------
-- +goose StatementBegin
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['cost_centers', 'budget_lines', 'cheques', 'cheque_events', 'currency_exchanges', 'fiscal_years'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
  END LOOP;
END
$$;
-- +goose StatementEnd
GRANT DELETE ON budget_lines TO paxve_app;

-- ---------------- profit & loss totals without the year-closing entries ----------------
-- +goose StatementBegin
CREATE FUNCTION pl_totals(p_company uuid, p_from date, p_to date)
RETURNS TABLE (account_id uuid, currency char(3), debit numeric, credit numeric, base_debit numeric, base_credit numeric)
LANGUAGE sql STABLE AS $$
  SELECT t.account_id, t.currency, sum(t.debit), sum(t.credit), sum(t.base_debit), sum(t.base_credit)
  FROM (
    SELECT a.account_id, a.currency, a.debit, a.credit, a.base_debit, a.base_credit
    FROM account_totals(p_company, p_from, p_to) a
    UNION ALL
    SELECT l.account_id, l.currency, -l.debit, -l.credit, -l.base_debit, -l.base_credit
    FROM journal_entries e JOIN journal_lines l ON l.entry_id = e.id
    WHERE e.company_id = p_company AND e.status = 'posted' AND e.entry_date BETWEEN p_from AND p_to
      AND (e.source = 'year_close'
           OR e.reversal_of IN (SELECT c.id FROM journal_entries c WHERE c.company_id = p_company AND c.source = 'year_close'))
  ) t
  GROUP BY t.account_id, t.currency
$$;
-- +goose StatementEnd
GRANT EXECUTE ON FUNCTION pl_totals(uuid, date, date) TO paxve_app;

-- +goose Down
DROP FUNCTION IF EXISTS pl_totals(uuid, date, date);
DROP TABLE IF EXISTS fiscal_years;
DROP TABLE IF EXISTS currency_exchanges;
DROP TABLE IF EXISTS cheque_events;
DROP TABLE IF EXISTS cheques;
ALTER TABLE payment_methods DROP CONSTRAINT payment_methods_kind_check;
ALTER TABLE payment_methods ADD CONSTRAINT payment_methods_kind_check CHECK (kind IN ('cash', 'wallet', 'bank', 'card'));
ALTER TABLE companies DROP COLUMN IF EXISTS cheque_in_hand_account_id, DROP COLUMN IF EXISTS cheque_collection_account_id,
  DROP COLUMN IF EXISTS cheque_payable_account_id, DROP COLUMN IF EXISTS retained_earnings_account_id;
DROP TABLE IF EXISTS budget_lines;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reverse_journal_entry(p_id uuid, p_date date, p_memo text) RETURNS uuid
LANGUAGE plpgsql AS $$
DECLARE
  v_orig journal_entries%ROWTYPE;
  v_new  uuid;
  v_lock date;
  v_date date;
BEGIN
  SELECT * INTO v_orig FROM journal_entries WHERE id = p_id FOR UPDATE;
  IF NOT FOUND OR v_orig.status <> 'posted' THEN
    RAISE EXCEPTION 'only posted entries can be reversed' USING ERRCODE = 'PXSTA';
  END IF;
  IF v_orig.reversed_by IS NOT NULL OR v_orig.reversal_of IS NOT NULL THEN
    RAISE EXCEPTION 'this entry is already reversed or is itself a reversal' USING ERRCODE = 'PXSTA';
  END IF;
  v_lock := company_lock_date(v_orig.company_id);
  v_date := coalesce(p_date, v_orig.entry_date);
  IF p_date IS NULL AND v_lock IS NOT NULL AND v_date <= v_lock THEN
    v_date := v_lock + 1;
  END IF;
  INSERT INTO journal_entries (company_id, entry_date, memo, reference, source, reversal_of)
  VALUES (v_orig.company_id, v_date,
          coalesce(nullif(p_memo, ''), 'Reversal of #' || v_orig.number), v_orig.reference, 'reversal', p_id)
  RETURNING id INTO v_new;
  INSERT INTO journal_lines (entry_id, line_no, account_id, description, currency, debit, credit, rate)
  SELECT v_new, line_no, account_id, description, currency, credit, debit, rate
  FROM journal_lines WHERE entry_id = p_id;
  PERFORM post_journal_entry(v_new);
  UPDATE journal_entries SET reversed_by = v_new WHERE id = p_id;
  RETURN v_new;
END
$$;
-- +goose StatementEnd
DROP INDEX IF EXISTS journal_lines_cost_center_idx;
ALTER TABLE journal_lines DROP COLUMN IF EXISTS cost_center_id;
DROP TABLE IF EXISTS cost_centers;
