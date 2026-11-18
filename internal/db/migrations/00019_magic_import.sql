-- Module O: migration from the legacy "Magic" system (VB.NET + SQL Server).
-- The owner exports his Magic database with tools/magic-export (SELECT-only) and uploads the zip here.
--   * external_ref ("magic:<kind>:<id>") marks every row created from Magic, so a second run never duplicates
--   * magic_imports keeps the uploaded file, the dry-run analysis and the result of each commit
--   * payments.kind 'opening_credit': a customer credit carried over from the old system (not money received
--     today, so it stays out of the collections / cash reports)

-- +goose Up

ALTER TABLE customers     ADD COLUMN external_ref text;
ALTER TABLE subscriptions ADD COLUMN external_ref text;
ALTER TABLE areas         ADD COLUMN external_ref text;
ALTER TABLE plans         ADD COLUMN external_ref text;
ALTER TABLE stations      ADD COLUMN external_ref text;
ALTER TABLE boxes         ADD COLUMN external_ref text;
ALTER TABLE suppliers     ADD COLUMN external_ref text;
ALTER TABLE invoices      ADD COLUMN external_ref text;
ALTER TABLE payments      ADD COLUMN external_ref text;
ALTER TABLE bills         ADD COLUMN external_ref text;
ALTER TABLE journal_entries ADD COLUMN external_ref text;
ALTER TABLE users         ADD COLUMN external_ref text;

CREATE UNIQUE INDEX customers_external_ref_key     ON customers (tenant_id, company_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE UNIQUE INDEX subscriptions_external_ref_key ON subscriptions (tenant_id, company_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE UNIQUE INDEX areas_external_ref_key         ON areas (tenant_id, company_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE UNIQUE INDEX plans_external_ref_key         ON plans (tenant_id, company_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE UNIQUE INDEX stations_external_ref_key      ON stations (tenant_id, company_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE UNIQUE INDEX boxes_external_ref_key         ON boxes (tenant_id, company_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE UNIQUE INDEX suppliers_external_ref_key     ON suppliers (tenant_id, company_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE UNIQUE INDEX invoices_external_ref_key      ON invoices (tenant_id, company_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE UNIQUE INDEX payments_external_ref_key      ON payments (tenant_id, company_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE UNIQUE INDEX bills_external_ref_key         ON bills (tenant_id, company_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE UNIQUE INDEX journal_entries_external_ref_key ON journal_entries (tenant_id, company_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE UNIQUE INDEX users_external_ref_key         ON users (tenant_id, external_ref) WHERE external_ref IS NOT NULL;

ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_kind_check;
ALTER TABLE payments ADD CONSTRAINT payments_kind_check CHECK (kind IN ('payment', 'credit_note', 'opening_credit'));
-- a credit note belongs to an invoice; payments and opening credits do not
-- +goose StatementBegin
DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT conname FROM pg_constraint WHERE conrelid = 'payments'::regclass AND contype = 'c'
             AND pg_get_constraintdef(oid) LIKE '%invoice_id IS NOT NULL%' LOOP
    EXECUTE format('ALTER TABLE payments DROP CONSTRAINT %I', r.conname);
  END LOOP;
END
$$;
-- +goose StatementEnd
ALTER TABLE payments ADD CONSTRAINT payments_credit_note_invoice CHECK (kind <> 'credit_note' OR invoice_id IS NOT NULL);

CREATE TABLE magic_imports (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  file_name   text NOT NULL DEFAULT '',
  size        bigint NOT NULL CHECK (size > 0 AND size <= 209715200),
  sha256      text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  received    bigint NOT NULL DEFAULT 0,
  data        bytea NOT NULL DEFAULT ''::bytea,
  status      text NOT NULL DEFAULT 'uploading'
              CHECK (status IN ('uploading', 'uploaded', 'invalid', 'running', 'done', 'failed')),
  analysis    jsonb,
  mapping     jsonb,
  options     jsonb,
  result      jsonb,
  progress    jsonb NOT NULL DEFAULT '{}'::jsonb,
  error       text,
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  UNIQUE (tenant_id, id)
);
CREATE INDEX magic_imports_tenant_idx ON magic_imports (tenant_id, created_at DESC);
CREATE TRIGGER magic_imports_touch BEFORE UPDATE ON magic_imports FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
ALTER TABLE magic_imports ENABLE ROW LEVEL SECURITY;
ALTER TABLE magic_imports FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON magic_imports USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
GRANT SELECT, INSERT, UPDATE ON magic_imports TO paxve_app;

-- +goose Down
DROP TABLE IF EXISTS magic_imports;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_credit_note_invoice;
ALTER TABLE payments ADD CHECK (kind = 'payment' OR invoice_id IS NOT NULL);
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_kind_check;
ALTER TABLE payments ADD CONSTRAINT payments_kind_check CHECK (kind IN ('payment', 'credit_note'));
DROP INDEX IF EXISTS customers_external_ref_key, subscriptions_external_ref_key, areas_external_ref_key, plans_external_ref_key,
  stations_external_ref_key, boxes_external_ref_key, suppliers_external_ref_key, invoices_external_ref_key,
  payments_external_ref_key, bills_external_ref_key, journal_entries_external_ref_key, users_external_ref_key;
ALTER TABLE customers DROP COLUMN IF EXISTS external_ref;
ALTER TABLE subscriptions DROP COLUMN IF EXISTS external_ref;
ALTER TABLE areas DROP COLUMN IF EXISTS external_ref;
ALTER TABLE plans DROP COLUMN IF EXISTS external_ref;
ALTER TABLE stations DROP COLUMN IF EXISTS external_ref;
ALTER TABLE boxes DROP COLUMN IF EXISTS external_ref;
ALTER TABLE suppliers DROP COLUMN IF EXISTS external_ref;
ALTER TABLE invoices DROP COLUMN IF EXISTS external_ref;
ALTER TABLE payments DROP COLUMN IF EXISTS external_ref;
ALTER TABLE bills DROP COLUMN IF EXISTS external_ref;
ALTER TABLE journal_entries DROP COLUMN IF EXISTS external_ref;
ALTER TABLE users DROP COLUMN IF EXISTS external_ref;
