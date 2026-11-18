-- Module L: purchase tax on expenses, void of stock documents, warehouse transfers, FX on later
-- allocations, credit notes / refunds / supplier debit notes, large billing runs in the background.

-- +goose Up

-- ---- tax on expenses (amount stays the total paid; tax_amount is the recoverable part of it) ----
ALTER TABLE expenses
  ADD COLUMN tax_rate_id uuid,
  ADD COLUMN tax_amount numeric(20, 4) NOT NULL DEFAULT 0 CHECK (tax_amount >= 0),
  ADD FOREIGN KEY (tenant_id, tax_rate_id) REFERENCES tax_rates (tenant_id, id),
  ADD CHECK (tax_amount < amount);

-- ---- stock documents can be voided (reversal) ----
ALTER TABLE stock_docs
  ADD COLUMN status text NOT NULL DEFAULT 'posted' CHECK (status IN ('posted', 'void')),
  ADD COLUMN void_entry_id uuid,
  ADD FOREIGN KEY (tenant_id, void_entry_id) REFERENCES journal_entries (tenant_id, id);
DROP TRIGGER stock_docs_immutable ON stock_docs;
CREATE TRIGGER stock_docs_guard BEFORE UPDATE OR DELETE ON stock_docs FOR EACH ROW EXECUTE FUNCTION posted_doc_guard();
DROP TRIGGER stock_docs_audit ON stock_docs;
CREATE TRIGGER stock_docs_audit AFTER INSERT OR UPDATE ON stock_docs FOR EACH ROW EXECUTE FUNCTION audit_row();

ALTER TABLE stock_moves DROP CONSTRAINT IF EXISTS stock_moves_source_check;
ALTER TABLE stock_moves ADD CONSTRAINT stock_moves_source_check CHECK (source IN ('bill', 'bill_void', 'fuel', 'adjust', 'sale', 'sale_void',
  'stock_void', 'transfer', 'transfer_void', 'debit_note', 'debit_note_void'));

-- ---- warehouse transfers (quantity moves at average cost; no ledger effect) ----
CREATE TABLE stock_transfers (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  number         bigint NOT NULL,
  transfer_date  date NOT NULL,
  item_id        uuid NOT NULL,
  from_warehouse uuid NOT NULL,
  to_warehouse   uuid NOT NULL,
  quantity       numeric(18, 4) NOT NULL CHECK (quantity > 0),
  value          numeric(20, 4) NOT NULL CHECK (value >= 0),
  status         text NOT NULL DEFAULT 'posted' CHECK (status IN ('posted', 'void')),
  void_entry_id  uuid,
  notes          text NOT NULL DEFAULT '',
  created_by     uuid DEFAULT app_user(),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, item_id) REFERENCES items (tenant_id, id),
  FOREIGN KEY (tenant_id, from_warehouse) REFERENCES warehouses (tenant_id, id),
  FOREIGN KEY (tenant_id, to_warehouse) REFERENCES warehouses (tenant_id, id),
  UNIQUE (tenant_id, company_id, number),
  CHECK (from_warehouse <> to_warehouse)
);
CREATE INDEX stock_transfers_date_idx ON stock_transfers (tenant_id, company_id, transfer_date);
CREATE TRIGGER stock_transfers_guard BEFORE UPDATE OR DELETE ON stock_transfers FOR EACH ROW EXECUTE FUNCTION posted_doc_guard();
CREATE TRIGGER stock_transfers_touch BEFORE UPDATE ON stock_transfers FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER stock_transfers_audit AFTER INSERT OR UPDATE ON stock_transfers FOR EACH ROW EXECUTE FUNCTION audit_row();

-- ---- customer payments: rate of the receivable credit + credit notes ----
-- ar_rate: rate (payment currency per base) at which the unapplied part of the payment was credited to receivables.
ALTER TABLE payments
  ADD COLUMN kind text NOT NULL DEFAULT 'payment' CHECK (kind IN ('payment', 'credit_note')),
  ADD COLUMN invoice_id uuid,
  ADD COLUMN ar_rate numeric(28, 10) CHECK (ar_rate > 0),
  ADD FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoices (tenant_id, id),
  ADD CHECK (kind = 'payment' OR invoice_id IS NOT NULL);
CREATE INDEX payments_invoice_idx ON payments (tenant_id, invoice_id) WHERE invoice_id IS NOT NULL;

-- FX difference booked when an earlier credit (advance / credit note) is applied to an invoice booked at another rate.
ALTER TABLE payment_allocations
  ADD COLUMN fx_entry_id uuid,
  ADD FOREIGN KEY (tenant_id, fx_entry_id) REFERENCES journal_entries (tenant_id, id);

-- ---- refunds (money paid back to a customer) are receivable debits, stored as invoices of kind 'refund' ----
ALTER TABLE invoices
  ADD COLUMN kind text NOT NULL DEFAULT 'invoice' CHECK (kind IN ('invoice', 'refund')),
  ADD COLUMN refund_method_id uuid,
  ADD FOREIGN KEY (tenant_id, refund_method_id) REFERENCES payment_methods (tenant_id, id);

-- ---- supplier debit notes (stored with supplier payments, like credit notes with customer payments) ----
ALTER TABLE supplier_payments
  ADD COLUMN kind text NOT NULL DEFAULT 'payment' CHECK (kind IN ('payment', 'debit_note')),
  ADD COLUMN bill_id uuid,
  ADD FOREIGN KEY (tenant_id, bill_id) REFERENCES bills (tenant_id, id),
  ALTER COLUMN method_id DROP NOT NULL,
  ADD CHECK (kind = 'debit_note' OR method_id IS NOT NULL),
  ADD CHECK (kind = 'payment' OR bill_id IS NOT NULL);

-- document numbers are counted per kind (credit notes / refunds / debit notes have their own sequence)
-- +goose StatementBegin
DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT c.conrelid::regclass::text AS tbl, c.conname FROM pg_constraint c
           WHERE c.contype = 'u' AND c.conrelid IN ('payments'::regclass, 'invoices'::regclass, 'supplier_payments'::regclass)
             AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                  WHERE a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)) = ARRAY['company_id', 'number', 'tenant_id'] LOOP
    EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', r.tbl, r.conname);
  END LOOP;
END
$$;
-- +goose StatementEnd
ALTER TABLE payments ADD CONSTRAINT payments_number_key UNIQUE (tenant_id, company_id, kind, number);
ALTER TABLE invoices ADD CONSTRAINT invoices_number_key UNIQUE (tenant_id, company_id, kind, number);
ALTER TABLE supplier_payments ADD CONSTRAINT supplier_payments_number_key UNIQUE (tenant_id, company_id, kind, number);

-- ---- billing runs processed in the background (large companies) ----
CREATE TABLE billing_jobs (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  period        text NOT NULL,
  params        jsonb NOT NULL DEFAULT '{}'::jsonb,
  status        text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done', 'failed')),
  total         int NOT NULL DEFAULT 0,
  processed     int NOT NULL DEFAULT 0,
  created       int NOT NULL DEFAULT 0,
  skipped_count int NOT NULL DEFAULT 0,
  skipped       jsonb NOT NULL DEFAULT '[]'::jsonb,
  totals        jsonb NOT NULL DEFAULT '{}'::jsonb,
  cursor_name   text,
  cursor_id     uuid,
  error         text NOT NULL DEFAULT '',
  created_by    uuid DEFAULT app_user(),
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  finished_at   timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id)
);
CREATE INDEX billing_jobs_company_idx ON billing_jobs (tenant_id, company_id, created_at DESC);
CREATE INDEX billing_jobs_running_idx ON billing_jobs (status) WHERE status = 'running';
CREATE TRIGGER billing_jobs_touch BEFORE UPDATE ON billing_jobs FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER billing_jobs_audit AFTER INSERT ON billing_jobs FOR EACH ROW EXECUTE FUNCTION audit_row();

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['stock_transfers', 'billing_jobs'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
  END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS billing_jobs, stock_transfers;
ALTER TABLE supplier_payments DROP CONSTRAINT IF EXISTS supplier_payments_number_key;
ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_number_key;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_number_key;
ALTER TABLE supplier_payments DROP COLUMN IF EXISTS kind, DROP COLUMN IF EXISTS bill_id;
ALTER TABLE invoices DROP COLUMN IF EXISTS kind, DROP COLUMN IF EXISTS refund_method_id;
ALTER TABLE payment_allocations DROP COLUMN IF EXISTS fx_entry_id;
ALTER TABLE payments DROP COLUMN IF EXISTS kind, DROP COLUMN IF EXISTS invoice_id, DROP COLUMN IF EXISTS ar_rate;
ALTER TABLE payments ADD UNIQUE (tenant_id, company_id, number);
ALTER TABLE invoices ADD UNIQUE (tenant_id, company_id, number);
ALTER TABLE supplier_payments ADD UNIQUE (tenant_id, company_id, number);
DROP TRIGGER IF EXISTS stock_docs_guard ON stock_docs;
CREATE TRIGGER stock_docs_immutable BEFORE UPDATE OR DELETE ON stock_docs FOR EACH ROW EXECUTE FUNCTION forbid_change();
ALTER TABLE stock_docs DROP COLUMN IF EXISTS status, DROP COLUMN IF EXISTS void_entry_id;
ALTER TABLE expenses DROP COLUMN IF EXISTS tax_rate_id, DROP COLUMN IF EXISTS tax_amount;
