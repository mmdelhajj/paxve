-- Module Q: company scope enforced by the database.
--   Every request under /companies/{cid}/... sets app.company_id; a user limited to some companies also gets
--   app.company_ids. A RESTRICTIVE policy on every company table then hides (and refuses to write) rows of any
--   other company, so a handler that forgets "AND company_id = $cid" can no longer read or change another
--   company's data. Line tables get their own company_id, copied from the parent document by a trigger
--   (and refused when the parent is not visible), so they are covered the same way.

-- +goose Up

-- +goose StatementBegin
CREATE FUNCTION app_company() RETURNS uuid
LANGUAGE sql STABLE PARALLEL SAFE AS $$
  SELECT nullif(current_setting('app.company_id', true), '')::uuid
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app_companies() RETURNS uuid[]
LANGUAGE sql STABLE PARALLEL SAFE AS $$
  SELECT nullif(current_setting('app.company_ids', true), '')::uuid[]
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION company_from_parent() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_parent uuid := (to_jsonb(NEW) ->> TG_ARGV[1])::uuid;
  v_company uuid;
BEGIN
  IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) ->> TG_ARGV[1]) IS NOT DISTINCT FROM (to_jsonb(NEW) ->> TG_ARGV[1])
     AND NEW.company_id IS NOT DISTINCT FROM OLD.company_id THEN
    RETURN NEW;
  END IF;
  EXECUTE format('SELECT company_id FROM %I WHERE id = $1', TG_ARGV[0]) INTO v_company USING v_parent;
  IF v_company IS NULL THEN
    RAISE EXCEPTION 'parent % not found', TG_ARGV[0] USING ERRCODE = 'PXNFD';
  END IF;
  NEW.company_id := v_company;
  RETURN NEW;
END
$$;
-- +goose StatementEnd

SET session_replication_role = replica;  -- backfill below must not fire the immutability / audit triggers
ALTER TABLE journal_lines ADD COLUMN company_id uuid;
UPDATE journal_lines c SET company_id = p.company_id FROM journal_entries p WHERE p.id = c.entry_id;
ALTER TABLE journal_lines ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER journal_lines_company BEFORE INSERT OR UPDATE ON journal_lines FOR EACH ROW EXECUTE FUNCTION company_from_parent('journal_entries', 'entry_id');

ALTER TABLE invoice_lines ADD COLUMN company_id uuid;
UPDATE invoice_lines c SET company_id = p.company_id FROM invoices p WHERE p.id = c.invoice_id;
ALTER TABLE invoice_lines ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER invoice_lines_company BEFORE INSERT OR UPDATE ON invoice_lines FOR EACH ROW EXECUTE FUNCTION company_from_parent('invoices', 'invoice_id');

ALTER TABLE meter_readings ADD COLUMN company_id uuid;
UPDATE meter_readings c SET company_id = p.company_id FROM subscriptions p WHERE p.id = c.subscription_id;
ALTER TABLE meter_readings ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER meter_readings_company BEFORE INSERT OR UPDATE ON meter_readings FOR EACH ROW EXECUTE FUNCTION company_from_parent('subscriptions', 'subscription_id');

ALTER TABLE payment_tenders ADD COLUMN company_id uuid;
UPDATE payment_tenders c SET company_id = p.company_id FROM payments p WHERE p.id = c.payment_id;
ALTER TABLE payment_tenders ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER payment_tenders_company BEFORE INSERT OR UPDATE ON payment_tenders FOR EACH ROW EXECUTE FUNCTION company_from_parent('payments', 'payment_id');

ALTER TABLE payment_allocations ADD COLUMN company_id uuid;
UPDATE payment_allocations c SET company_id = p.company_id FROM payments p WHERE p.id = c.payment_id;
ALTER TABLE payment_allocations ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER payment_allocations_company BEFORE INSERT OR UPDATE ON payment_allocations FOR EACH ROW EXECUTE FUNCTION company_from_parent('payments', 'payment_id');

ALTER TABLE credit_note_lines ADD COLUMN company_id uuid;
UPDATE credit_note_lines c SET company_id = p.company_id FROM payments p WHERE p.id = c.payment_id;
ALTER TABLE credit_note_lines ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER credit_note_lines_company BEFORE INSERT OR UPDATE ON credit_note_lines FOR EACH ROW EXECUTE FUNCTION company_from_parent('payments', 'payment_id');

ALTER TABLE stock ADD COLUMN company_id uuid;
UPDATE stock c SET company_id = p.company_id FROM warehouses p WHERE p.id = c.warehouse_id;
ALTER TABLE stock ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER stock_company BEFORE INSERT OR UPDATE ON stock FOR EACH ROW EXECUTE FUNCTION company_from_parent('warehouses', 'warehouse_id');

ALTER TABLE item_prices ADD COLUMN company_id uuid;
UPDATE item_prices c SET company_id = p.company_id FROM items p WHERE p.id = c.item_id;
ALTER TABLE item_prices ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER item_prices_company BEFORE INSERT OR UPDATE ON item_prices FOR EACH ROW EXECUTE FUNCTION company_from_parent('items', 'item_id');

ALTER TABLE bill_lines ADD COLUMN company_id uuid;
UPDATE bill_lines c SET company_id = p.company_id FROM bills p WHERE p.id = c.bill_id;
ALTER TABLE bill_lines ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER bill_lines_company BEFORE INSERT OR UPDATE ON bill_lines FOR EACH ROW EXECUTE FUNCTION company_from_parent('bills', 'bill_id');

ALTER TABLE supplier_allocations ADD COLUMN company_id uuid;
UPDATE supplier_allocations c SET company_id = p.company_id FROM supplier_payments p WHERE p.id = c.payment_id;
ALTER TABLE supplier_allocations ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER supplier_allocations_company BEFORE INSERT OR UPDATE ON supplier_allocations FOR EACH ROW EXECUTE FUNCTION company_from_parent('supplier_payments', 'payment_id');

ALTER TABLE generator_hours ADD COLUMN company_id uuid;
UPDATE generator_hours c SET company_id = p.company_id FROM generators p WHERE p.id = c.generator_id;
ALTER TABLE generator_hours ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER generator_hours_company BEFORE INSERT OR UPDATE ON generator_hours FOR EACH ROW EXECUTE FUNCTION company_from_parent('generators', 'generator_id');

ALTER TABLE outages ADD COLUMN company_id uuid;
UPDATE outages c SET company_id = p.company_id FROM generators p WHERE p.id = c.generator_id;
ALTER TABLE outages ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER outages_company BEFORE INSERT OR UPDATE ON outages FOR EACH ROW EXECUTE FUNCTION company_from_parent('generators', 'generator_id');

ALTER TABLE payslips ADD COLUMN company_id uuid;
UPDATE payslips c SET company_id = p.company_id FROM payroll_runs p WHERE p.id = c.run_id;
ALTER TABLE payslips ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER payslips_company BEFORE INSERT OR UPDATE ON payslips FOR EACH ROW EXECUTE FUNCTION company_from_parent('payroll_runs', 'run_id');

ALTER TABLE depreciation_lines ADD COLUMN company_id uuid;
UPDATE depreciation_lines c SET company_id = p.company_id FROM assets p WHERE p.id = c.asset_id;
ALTER TABLE depreciation_lines ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER depreciation_lines_company BEFORE INSERT OR UPDATE ON depreciation_lines FOR EACH ROW EXECUTE FUNCTION company_from_parent('assets', 'asset_id');

ALTER TABLE bank_statement_lines ADD COLUMN company_id uuid;
UPDATE bank_statement_lines c SET company_id = p.company_id FROM bank_statements p WHERE p.id = c.statement_id;
ALTER TABLE bank_statement_lines ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER bank_statement_lines_company BEFORE INSERT OR UPDATE ON bank_statement_lines FOR EACH ROW EXECUTE FUNCTION company_from_parent('bank_statements', 'statement_id');

ALTER TABLE sales_order_lines ADD COLUMN company_id uuid;
UPDATE sales_order_lines c SET company_id = p.company_id FROM sales_orders p WHERE p.id = c.order_id;
ALTER TABLE sales_order_lines ALTER COLUMN company_id SET NOT NULL;
CREATE TRIGGER sales_order_lines_company BEFORE INSERT OR UPDATE ON sales_order_lines FOR EACH ROW EXECUTE FUNCTION company_from_parent('sales_orders', 'order_id');

SET session_replication_role = origin;

-- Same-company references: every foreign key between two company tables also has to match on company_id,
-- so a document can never point to a customer, account, item... of another company (foreign key checks
-- ignore row-level security, so the policy alone would not stop that).
-- +goose StatementBegin
DO $$
DECLARE
  r record;
  v_src text;
  v_n int := 0;
BEGIN
  FOR r IN
    SELECT c.conname, c.conrelid::regclass::text AS tbl, c.confrelid::regclass::text AS ref,
           (SELECT a.attname FROM pg_attribute a WHERE a.attrelid = c.conrelid AND a.attnum = c.conkey[array_length(c.conkey, 1)]) AS col
    FROM pg_constraint c
    WHERE c.contype = 'f'
      AND (SELECT a.attname FROM pg_attribute a WHERE a.attrelid = c.confrelid AND a.attnum = c.confkey[array_length(c.confkey, 1)]) = 'id'
      AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.confrelid AND a.attname = 'company_id' AND NOT a.attisdropped)
      AND (c.conrelid = 'companies'::regclass OR
           EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.conrelid AND a.attname = 'company_id' AND NOT a.attisdropped))
      AND (SELECT a.attname FROM pg_attribute a WHERE a.attrelid = c.conrelid AND a.attnum = c.conkey[array_length(c.conkey, 1)]) <> 'company_id'
    ORDER BY 2, 4
  LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = r.ref || '_company_id_key') THEN
      EXECUTE format('CREATE UNIQUE INDEX %I ON %I (company_id, id)', r.ref || '_company_id_key', r.ref);
      EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I UNIQUE USING INDEX %I', r.ref, r.ref || '_company_id_key', r.ref || '_company_id_key');
    END IF;
    v_src := CASE WHEN r.tbl = 'companies' THEN 'id' ELSE 'company_id' END;
    EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I FOREIGN KEY (%I, %I) REFERENCES %I (company_id, id)',
                   r.tbl, left(r.tbl || '_' || r.col || '_same_company', 63), v_src, r.col, r.ref);
    v_n := v_n + 1;
  END LOOP;
  RAISE NOTICE 'same-company foreign keys: %', v_n;
END
$$;
-- +goose StatementEnd
CREATE POLICY company_scope ON companies AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON accounts AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON approval_requests AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON areas AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON assets AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON bank_statements AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON billing_jobs AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON bills AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON boxes AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON cash_transfers AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON crm_activities AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON crm_leads AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON crm_stages AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON customers AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON doc_counters AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON employee_advances AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON employees AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON exchange_rates AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON expenses AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON generators AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON invoices AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON item_categories AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON items AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON journal_counters AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON journal_entries AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON message_providers AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON message_templates AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON messages AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON payment_methods AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON payments AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON payroll_payments AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON payroll_runs AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON plans AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON pos_orders AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON pos_registers AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON pos_sessions AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON recurring_templates AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON router_actions AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON routers AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON sales_deliveries AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON sales_orders AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON stations AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON stock_docs AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON stock_moves AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON stock_transfers AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON subscriptions AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON supplier_payments AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON suppliers AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON tariffs AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON tax_rates AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON warehouses AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON webhook_deliveries AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON webhooks AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON journal_lines AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON invoice_lines AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON meter_readings AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON payment_tenders AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON payment_allocations AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON credit_note_lines AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON stock AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON item_prices AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON bill_lines AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON supplier_allocations AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON generator_hours AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON outages AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON payslips AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON depreciation_lines AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON bank_statement_lines AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));
CREATE POLICY company_scope ON sales_order_lines AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])));

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT conrelid::regclass::text AS tbl, conname FROM pg_constraint WHERE conname LIKE '%\_same\_company' LOOP
    EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', r.tbl, r.conname);
  END LOOP;
  FOR r IN SELECT conrelid::regclass::text AS tbl, conname FROM pg_constraint WHERE conname LIKE '%\_company\_id\_key' LOOP
    EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', r.tbl, r.conname);
  END LOOP;
END
$$;
-- +goose StatementEnd
DROP POLICY IF EXISTS company_scope ON companies;
DROP POLICY IF EXISTS company_scope ON accounts;
DROP POLICY IF EXISTS company_scope ON approval_requests;
DROP POLICY IF EXISTS company_scope ON areas;
DROP POLICY IF EXISTS company_scope ON assets;
DROP POLICY IF EXISTS company_scope ON bank_statements;
DROP POLICY IF EXISTS company_scope ON billing_jobs;
DROP POLICY IF EXISTS company_scope ON bills;
DROP POLICY IF EXISTS company_scope ON boxes;
DROP POLICY IF EXISTS company_scope ON cash_transfers;
DROP POLICY IF EXISTS company_scope ON crm_activities;
DROP POLICY IF EXISTS company_scope ON crm_leads;
DROP POLICY IF EXISTS company_scope ON crm_stages;
DROP POLICY IF EXISTS company_scope ON customers;
DROP POLICY IF EXISTS company_scope ON doc_counters;
DROP POLICY IF EXISTS company_scope ON employee_advances;
DROP POLICY IF EXISTS company_scope ON employees;
DROP POLICY IF EXISTS company_scope ON exchange_rates;
DROP POLICY IF EXISTS company_scope ON expenses;
DROP POLICY IF EXISTS company_scope ON generators;
DROP POLICY IF EXISTS company_scope ON invoices;
DROP POLICY IF EXISTS company_scope ON item_categories;
DROP POLICY IF EXISTS company_scope ON items;
DROP POLICY IF EXISTS company_scope ON journal_counters;
DROP POLICY IF EXISTS company_scope ON journal_entries;
DROP POLICY IF EXISTS company_scope ON message_providers;
DROP POLICY IF EXISTS company_scope ON message_templates;
DROP POLICY IF EXISTS company_scope ON messages;
DROP POLICY IF EXISTS company_scope ON payment_methods;
DROP POLICY IF EXISTS company_scope ON payments;
DROP POLICY IF EXISTS company_scope ON payroll_payments;
DROP POLICY IF EXISTS company_scope ON payroll_runs;
DROP POLICY IF EXISTS company_scope ON plans;
DROP POLICY IF EXISTS company_scope ON pos_orders;
DROP POLICY IF EXISTS company_scope ON pos_registers;
DROP POLICY IF EXISTS company_scope ON pos_sessions;
DROP POLICY IF EXISTS company_scope ON recurring_templates;
DROP POLICY IF EXISTS company_scope ON router_actions;
DROP POLICY IF EXISTS company_scope ON routers;
DROP POLICY IF EXISTS company_scope ON sales_deliveries;
DROP POLICY IF EXISTS company_scope ON sales_orders;
DROP POLICY IF EXISTS company_scope ON stations;
DROP POLICY IF EXISTS company_scope ON stock_docs;
DROP POLICY IF EXISTS company_scope ON stock_moves;
DROP POLICY IF EXISTS company_scope ON stock_transfers;
DROP POLICY IF EXISTS company_scope ON subscriptions;
DROP POLICY IF EXISTS company_scope ON supplier_payments;
DROP POLICY IF EXISTS company_scope ON suppliers;
DROP POLICY IF EXISTS company_scope ON tariffs;
DROP POLICY IF EXISTS company_scope ON tax_rates;
DROP POLICY IF EXISTS company_scope ON warehouses;
DROP POLICY IF EXISTS company_scope ON webhook_deliveries;
DROP POLICY IF EXISTS company_scope ON webhooks;
DROP POLICY IF EXISTS company_scope ON journal_lines;
DROP POLICY IF EXISTS company_scope ON invoice_lines;
DROP POLICY IF EXISTS company_scope ON meter_readings;
DROP POLICY IF EXISTS company_scope ON payment_tenders;
DROP POLICY IF EXISTS company_scope ON payment_allocations;
DROP POLICY IF EXISTS company_scope ON credit_note_lines;
DROP POLICY IF EXISTS company_scope ON stock;
DROP POLICY IF EXISTS company_scope ON item_prices;
DROP POLICY IF EXISTS company_scope ON bill_lines;
DROP POLICY IF EXISTS company_scope ON supplier_allocations;
DROP POLICY IF EXISTS company_scope ON generator_hours;
DROP POLICY IF EXISTS company_scope ON outages;
DROP POLICY IF EXISTS company_scope ON payslips;
DROP POLICY IF EXISTS company_scope ON depreciation_lines;
DROP POLICY IF EXISTS company_scope ON bank_statement_lines;
DROP POLICY IF EXISTS company_scope ON sales_order_lines;
DROP TRIGGER IF EXISTS journal_lines_company ON journal_lines;
ALTER TABLE journal_lines DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS invoice_lines_company ON invoice_lines;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS meter_readings_company ON meter_readings;
ALTER TABLE meter_readings DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS payment_tenders_company ON payment_tenders;
ALTER TABLE payment_tenders DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS payment_allocations_company ON payment_allocations;
ALTER TABLE payment_allocations DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS credit_note_lines_company ON credit_note_lines;
ALTER TABLE credit_note_lines DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS stock_company ON stock;
ALTER TABLE stock DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS item_prices_company ON item_prices;
ALTER TABLE item_prices DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS bill_lines_company ON bill_lines;
ALTER TABLE bill_lines DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS supplier_allocations_company ON supplier_allocations;
ALTER TABLE supplier_allocations DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS generator_hours_company ON generator_hours;
ALTER TABLE generator_hours DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS outages_company ON outages;
ALTER TABLE outages DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS payslips_company ON payslips;
ALTER TABLE payslips DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS depreciation_lines_company ON depreciation_lines;
ALTER TABLE depreciation_lines DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS bank_statement_lines_company ON bank_statement_lines;
ALTER TABLE bank_statement_lines DROP COLUMN IF EXISTS company_id;
DROP TRIGGER IF EXISTS sales_order_lines_company ON sales_order_lines;
ALTER TABLE sales_order_lines DROP COLUMN IF EXISTS company_id;
DROP FUNCTION IF EXISTS company_from_parent();
DROP FUNCTION IF EXISTS app_companies();
DROP FUNCTION IF EXISTS app_company();
