-- Module Q: fast financial reports on large books.
--   ledger_daily holds, per company / day / account / currency, the totals of the posted journal lines.
--   A background worker adds newly posted entries in journal number order (posted entries get gap-free
--   numbers in commit order and never change again), and remembers the last number it added
--   (ledger_watermark). account_totals() = ledger_daily for the range + the few entries posted since.

-- +goose Up
CREATE TABLE ledger_daily (
  tenant_id   uuid NOT NULL REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  day         date NOT NULL,
  account_id  uuid NOT NULL,
  currency    char(3) NOT NULL,
  debit       numeric NOT NULL DEFAULT 0,
  credit      numeric NOT NULL DEFAULT 0,
  base_debit  numeric NOT NULL DEFAULT 0,
  base_credit numeric NOT NULL DEFAULT 0,
  entries     bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (company_id, day, account_id, currency)
);
CREATE INDEX ledger_daily_account_idx ON ledger_daily (company_id, account_id, day);

CREATE TABLE ledger_watermark (
  company_id  uuid PRIMARY KEY,
  tenant_id   uuid NOT NULL REFERENCES tenants (id),
  last_number bigint NOT NULL DEFAULT 0,
  updated_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE ledger_daily ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_daily FORCE ROW LEVEL SECURITY;
ALTER TABLE ledger_watermark ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_watermark FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ledger_daily USING (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON ledger_watermark USING (tenant_id = app_tenant());
CREATE POLICY company_scope ON ledger_daily AS RESTRICTIVE FOR ALL
  USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON ledger_watermark AS RESTRICTIVE FOR ALL
  USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
GRANT SELECT ON ledger_daily, ledger_watermark TO paxve_app;   -- written only by the worker (owner connection)

CREATE INDEX IF NOT EXISTS journal_entries_posted_number_idx ON journal_entries (company_id, number) WHERE status = 'posted';

-- totals of posted lines per account and currency, entry dates between p_from and p_to
-- +goose StatementBegin
CREATE FUNCTION account_totals(p_company uuid, p_from date, p_to date)
RETURNS TABLE (account_id uuid, currency char(3), debit numeric, credit numeric, base_debit numeric, base_credit numeric, entries bigint)
LANGUAGE sql STABLE AS $$
  WITH wm AS (SELECT coalesce((SELECT w.last_number FROM ledger_watermark w WHERE w.company_id = p_company), 0) AS n),
  t AS (
    SELECT d.account_id, d.currency, d.debit, d.credit, d.base_debit, d.base_credit, d.entries
    FROM ledger_daily d WHERE d.company_id = p_company AND d.day BETWEEN p_from AND p_to
    UNION ALL
    SELECT l.account_id, l.currency, sum(l.debit), sum(l.credit), sum(l.base_debit), sum(l.base_credit), count(DISTINCT e.id)
    FROM journal_entries e JOIN journal_lines l ON l.entry_id = e.id
    WHERE e.company_id = p_company AND e.status = 'posted' AND e.number > (SELECT n FROM wm)
      AND e.entry_date BETWEEN p_from AND p_to
    GROUP BY l.account_id, l.currency
  )
  SELECT t.account_id, t.currency, sum(t.debit), sum(t.credit), sum(t.base_debit), sum(t.base_credit), sum(t.entries)::bigint
  FROM t GROUP BY t.account_id, t.currency
$$;
-- +goose StatementEnd
GRANT EXECUTE ON FUNCTION account_totals(uuid, date, date) TO paxve_app;

-- adds up to p_batch newly posted entries of one company to ledger_daily; returns how many it added
-- (called by the worker as the owner; the advisory lock keeps two app instances from adding twice)
-- +goose StatementBegin
CREATE FUNCTION ledger_catch_up(p_company uuid, p_batch int) RETURNS int
LANGUAGE plpgsql AS $$
DECLARE
  v_tenant uuid;
  v_from   bigint;
  v_to     bigint;
  v_n      int;
BEGIN
  IF NOT pg_try_advisory_xact_lock(hashtext('ledger_catch_up'), hashtext(p_company::text)) THEN
    RETURN 0;
  END IF;
  SELECT tenant_id INTO v_tenant FROM companies WHERE id = p_company;
  INSERT INTO ledger_watermark (company_id, tenant_id) VALUES (p_company, v_tenant) ON CONFLICT DO NOTHING;
  SELECT last_number INTO v_from FROM ledger_watermark WHERE company_id = p_company FOR UPDATE;
  SELECT max(number), count(*) INTO v_to, v_n FROM (
    SELECT number FROM journal_entries WHERE company_id = p_company AND status = 'posted' AND number > v_from
    ORDER BY number LIMIT p_batch) x;
  IF v_n = 0 THEN
    RETURN 0;
  END IF;
  INSERT INTO ledger_daily AS d (tenant_id, company_id, day, account_id, currency, debit, credit, base_debit, base_credit, entries)
  SELECT v_tenant, p_company, e.entry_date, l.account_id, l.currency, sum(l.debit), sum(l.credit), sum(l.base_debit), sum(l.base_credit),
         count(DISTINCT e.id)
  FROM journal_entries e JOIN journal_lines l ON l.entry_id = e.id
  WHERE e.company_id = p_company AND e.status = 'posted' AND e.number > v_from AND e.number <= v_to
  GROUP BY e.entry_date, l.account_id, l.currency
  ORDER BY e.entry_date, l.account_id, l.currency
  ON CONFLICT (company_id, day, account_id, currency) DO UPDATE SET
    debit = d.debit + excluded.debit, credit = d.credit + excluded.credit,
    base_debit = d.base_debit + excluded.base_debit, base_credit = d.base_credit + excluded.base_credit,
    entries = d.entries + excluded.entries;
  UPDATE ledger_watermark SET last_number = v_to, updated_at = now() WHERE company_id = p_company;
  RETURN v_n;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION ledger_catch_up(uuid, int) FROM PUBLIC;

-- +goose Down
DROP FUNCTION IF EXISTS ledger_catch_up(uuid, int);
DROP FUNCTION IF EXISTS account_totals(uuid, date, date);
DROP INDEX IF EXISTS journal_entries_posted_number_idx;
DROP TABLE IF EXISTS ledger_watermark;
DROP TABLE IF EXISTS ledger_daily;
