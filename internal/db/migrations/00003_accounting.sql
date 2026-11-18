-- Accounting core: chart of accounts, journal entries, journal lines.
-- Rules enforced here, not only in the app:
--   * a posted entry has at least 2 lines and debits = credits (in base currency)
--   * posted entries and their lines can never be changed or deleted, only reversed
--   * entry numbers are gap-free per company (assigned at posting)
--   * money is numeric, never float

-- +goose Up

CREATE TABLE accounts (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  code        text NOT NULL CHECK (code ~ '^[0-9A-Za-z.\-]{1,20}$'),
  name        text NOT NULL CHECK (length(trim(name)) > 0),
  name_i18n   jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(name_i18n) = 'object'),
  type        text NOT NULL CHECK (type IN ('asset', 'liability', 'equity', 'income', 'expense')),
  parent_id   uuid,
  is_postable boolean NOT NULL DEFAULT true,
  currency    char(3) REFERENCES currencies (code),
  is_active   boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, parent_id) REFERENCES accounts (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, code)
);
CREATE INDEX accounts_parent_idx ON accounts (tenant_id, parent_id);

CREATE TABLE journal_entries (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  number      bigint,
  entry_date  date NOT NULL,
  memo        text NOT NULL DEFAULT '',
  reference   text NOT NULL DEFAULT '',
  status      text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'posted')),
  source      text NOT NULL DEFAULT 'manual',
  reversal_of uuid,
  reversed_by uuid,
  created_by  uuid DEFAULT app_user(),
  posted_by   uuid,
  posted_at   timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, reversal_of) REFERENCES journal_entries (tenant_id, id),
  FOREIGN KEY (tenant_id, reversed_by) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, number),
  CHECK ((status = 'posted') = (number IS NOT NULL))
);
CREATE INDEX journal_entries_list_idx ON journal_entries (tenant_id, company_id, entry_date DESC, number DESC);

CREATE TABLE journal_lines (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  entry_id    uuid NOT NULL,
  line_no     int NOT NULL CHECK (line_no > 0),
  account_id  uuid NOT NULL,
  description text NOT NULL DEFAULT '',
  currency    char(3) NOT NULL REFERENCES currencies (code),
  debit       numeric(20, 4) NOT NULL DEFAULT 0 CHECK (debit >= 0),
  credit      numeric(20, 4) NOT NULL DEFAULT 0 CHECK (credit >= 0),
  rate        numeric(28, 10) NOT NULL DEFAULT 1 CHECK (rate > 0),
  base_debit  numeric(20, 4) NOT NULL DEFAULT 0,
  base_credit numeric(20, 4) NOT NULL DEFAULT 0,
  FOREIGN KEY (tenant_id, entry_id) REFERENCES journal_entries (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id),
  UNIQUE (tenant_id, entry_id, line_no),
  CHECK ((debit = 0) <> (credit = 0))
);
CREATE INDEX journal_lines_account_idx ON journal_lines (tenant_id, account_id);

-- Gap-free numbering: one counter row per company, incremented inside the posting transaction.
CREATE TABLE journal_counters (
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  last_number bigint NOT NULL,
  PRIMARY KEY (tenant_id, company_id),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id)
);

CREATE TRIGGER accounts_touch BEFORE UPDATE ON accounts FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER journal_entries_touch BEFORE UPDATE ON journal_entries FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- Base-currency amounts are always computed by the database: amount / rate, rounded to 4 places.
-- +goose StatementBegin
CREATE FUNCTION journal_line_compute() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.base_debit  := round(NEW.debit / NEW.rate, 4);
  NEW.base_credit := round(NEW.credit / NEW.rate, 4);
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER journal_lines_compute BEFORE INSERT OR UPDATE ON journal_lines
  FOR EACH ROW EXECUTE FUNCTION journal_line_compute();

-- Posted entries are immutable. The only allowed change is linking the reversing entry once.
-- +goose StatementBegin
CREATE FUNCTION journal_entry_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'draft' THEN
      RAISE EXCEPTION 'journal entries are created as drafts and posted afterwards' USING ERRCODE = 'PXSTA';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'posted' THEN
    IF TG_OP = 'DELETE' THEN
      RAISE EXCEPTION 'posted journal entries cannot be deleted; reverse them instead' USING ERRCODE = 'PXIMM';
    END IF;
    IF OLD.reversed_by IS NULL AND NEW.reversed_by IS NOT NULL
       AND (to_jsonb(NEW) - 'reversed_by' - 'updated_at') = (to_jsonb(OLD) - 'reversed_by' - 'updated_at') THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'posted journal entries cannot be changed; reverse them instead' USING ERRCODE = 'PXIMM';
  END IF;
  IF TG_OP = 'UPDATE' AND NEW.company_id <> OLD.company_id THEN
    RAISE EXCEPTION 'an entry cannot move to another company' USING ERRCODE = 'PXSTA';
  END IF;
  RETURN coalesce(NEW, OLD);
END
$$;
-- +goose StatementEnd
CREATE TRIGGER journal_entries_guard BEFORE INSERT OR UPDATE OR DELETE ON journal_entries
  FOR EACH ROW EXECUTE FUNCTION journal_entry_guard();

-- +goose StatementBegin
CREATE FUNCTION journal_line_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_status text;
BEGIN
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
CREATE TRIGGER journal_lines_guard BEFORE INSERT OR UPDATE OR DELETE ON journal_lines
  FOR EACH ROW EXECUTE FUNCTION journal_line_guard();

-- Balance check when an entry becomes posted (deferred to commit, and forced
-- immediately inside post_journal_entry so the error reaches the caller).
-- +goose StatementBegin
CREATE FUNCTION journal_entry_check_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_lines int;
  v_dr    numeric;
  v_cr    numeric;
  v_bad   int;
BEGIN
  SELECT count(*), coalesce(sum(base_debit), 0), coalesce(sum(base_credit), 0)
    INTO v_lines, v_dr, v_cr
  FROM journal_lines WHERE entry_id = NEW.id;

  IF v_lines < 2 OR v_dr <> v_cr OR v_dr = 0 THEN
    RAISE EXCEPTION 'journal entry is not balanced: % lines, debit %, credit %', v_lines, v_dr, v_cr
      USING ERRCODE = 'PXUNB';
  END IF;

  SELECT count(*) INTO v_bad
  FROM journal_lines l
  JOIN accounts a ON a.id = l.account_id
  WHERE l.entry_id = NEW.id
    AND (a.company_id <> NEW.company_id OR NOT a.is_postable OR NOT a.is_active
         OR (a.currency IS NOT NULL AND a.currency <> l.currency));
  IF v_bad > 0 THEN
    RAISE EXCEPTION 'journal entry uses % account line(s) that cannot be posted to', v_bad
      USING ERRCODE = 'PXACC';
  END IF;
  RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER journal_entries_balanced
  AFTER UPDATE OF status ON journal_entries
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW WHEN (NEW.status = 'posted')
  EXECUTE FUNCTION journal_entry_check_balanced();

-- +goose StatementBegin
CREATE FUNCTION post_journal_entry(p_id uuid) RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
  v_company uuid;
  v_number  bigint;
BEGIN
  SELECT company_id INTO v_company FROM journal_entries WHERE id = p_id AND status = 'draft' FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'journal entry not found or already posted' USING ERRCODE = 'PXSTA';
  END IF;

  INSERT INTO journal_counters (tenant_id, company_id, last_number)
  VALUES (app_tenant(), v_company, 1)
  ON CONFLICT (tenant_id, company_id) DO UPDATE SET last_number = journal_counters.last_number + 1
  RETURNING last_number INTO v_number;

  UPDATE journal_entries
  SET status = 'posted', number = v_number, posted_at = now(), posted_by = app_user()
  WHERE id = p_id;

  SET CONSTRAINTS journal_entries_balanced IMMEDIATE;
  SET CONSTRAINTS journal_entries_balanced DEFERRED;
  RETURN v_number;
END
$$;
-- +goose StatementEnd

-- Reversal: a new posted entry with debit and credit swapped, linked both ways.
-- +goose StatementBegin
CREATE FUNCTION reverse_journal_entry(p_id uuid, p_date date, p_memo text) RETURNS uuid
LANGUAGE plpgsql AS $$
DECLARE
  v_orig journal_entries%ROWTYPE;
  v_new  uuid;
BEGIN
  SELECT * INTO v_orig FROM journal_entries WHERE id = p_id FOR UPDATE;
  IF NOT FOUND OR v_orig.status <> 'posted' THEN
    RAISE EXCEPTION 'only posted entries can be reversed' USING ERRCODE = 'PXSTA';
  END IF;
  IF v_orig.reversed_by IS NOT NULL OR v_orig.reversal_of IS NOT NULL THEN
    RAISE EXCEPTION 'this entry is already reversed or is itself a reversal' USING ERRCODE = 'PXSTA';
  END IF;

  INSERT INTO journal_entries (company_id, entry_date, memo, reference, source, reversal_of)
  VALUES (v_orig.company_id, coalesce(p_date, v_orig.entry_date),
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

CREATE TRIGGER accounts_audit AFTER INSERT OR UPDATE OR DELETE ON accounts FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER journal_entries_audit AFTER INSERT OR UPDATE OR DELETE ON journal_entries FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER journal_lines_audit AFTER INSERT OR UPDATE OR DELETE ON journal_lines FOR EACH ROW EXECUTE FUNCTION audit_row();

ALTER TABLE accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts FORCE ROW LEVEL SECURITY;
ALTER TABLE journal_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE journal_entries FORCE ROW LEVEL SECURITY;
ALTER TABLE journal_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE journal_lines FORCE ROW LEVEL SECURITY;
ALTER TABLE journal_counters ENABLE ROW LEVEL SECURITY;
ALTER TABLE journal_counters FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON accounts USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON journal_entries USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON journal_lines USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON journal_counters USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());

GRANT SELECT, INSERT, UPDATE ON accounts, journal_counters TO paxve_app;
-- DELETE is granted only so drafts can be discarded; the guard trigger blocks it for posted entries.
GRANT SELECT, INSERT, UPDATE, DELETE ON journal_entries, journal_lines TO paxve_app;

-- +goose Down
DROP FUNCTION IF EXISTS reverse_journal_entry(uuid, date, text);
DROP FUNCTION IF EXISTS post_journal_entry(uuid);
DROP TABLE IF EXISTS journal_counters;
DROP TABLE IF EXISTS journal_lines;
DROP TABLE IF EXISTS journal_entries;
DROP TABLE IF EXISTS accounts;
DROP FUNCTION IF EXISTS journal_entry_check_balanced();
DROP FUNCTION IF EXISTS journal_line_guard();
DROP FUNCTION IF EXISTS journal_entry_guard();
DROP FUNCTION IF EXISTS journal_line_compute();
