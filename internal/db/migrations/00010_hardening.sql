-- Hardening: period lock (closed books) and cash rounding for change.
--   * companies.lock_date: nothing can be created, changed or posted in the journal on or before
--     this date (so no invoice, payment, bill, expense... either, as they all post entries).
--     Reversals of locked entries are dated after the lock automatically.
--   * payment_methods.change_rounding: change given from this box is rounded DOWN to this step
--     (e.g. 1000 or 5000 LBP), the remainder is booked as exchange/rounding gain.

-- +goose Up

ALTER TABLE companies ADD COLUMN lock_date date;
ALTER TABLE payment_methods ADD COLUMN change_rounding numeric(20, 4) CHECK (change_rounding IS NULL OR change_rounding > 0);

-- +goose StatementBegin
CREATE FUNCTION company_lock_date(p_company uuid) RETURNS date
LANGUAGE sql STABLE AS $$
  SELECT lock_date FROM companies WHERE id = p_company
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION journal_entry_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_lock date;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'draft' THEN
      RAISE EXCEPTION 'journal entries are created as drafts and posted afterwards' USING ERRCODE = 'PXSTA';
    END IF;
    v_lock := company_lock_date(NEW.company_id);
    IF v_lock IS NOT NULL AND NEW.entry_date <= v_lock THEN
      RAISE EXCEPTION 'the books are locked up to %', v_lock USING ERRCODE = 'PXLCK';
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
  IF TG_OP = 'UPDATE' THEN
    IF NEW.company_id <> OLD.company_id THEN
      RAISE EXCEPTION 'an entry cannot move to another company' USING ERRCODE = 'PXSTA';
    END IF;
    v_lock := company_lock_date(NEW.company_id);
    IF v_lock IS NOT NULL AND (NEW.entry_date <= v_lock OR OLD.entry_date <= v_lock) THEN
      RAISE EXCEPTION 'the books are locked up to %', v_lock USING ERRCODE = 'PXLCK';
    END IF;
  END IF;
  RETURN coalesce(NEW, OLD);
END
$$;
-- +goose StatementEnd

-- Reversal: dated p_date, or the original date, but never inside the locked period.
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

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION company_lock_date(p_company uuid) RETURNS date
LANGUAGE sql STABLE AS $$ SELECT NULL::date $$;
-- +goose StatementEnd
ALTER TABLE payment_methods DROP COLUMN IF EXISTS change_rounding;
ALTER TABLE companies DROP COLUMN IF EXISTS lock_date;
