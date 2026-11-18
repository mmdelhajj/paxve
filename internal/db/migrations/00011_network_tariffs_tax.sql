-- Generator / ISP network, generator operations log, official tariffs, bills in a second
-- currency (LBP snapshot), and taxes (VAT) on invoices and purchase bills.

-- +goose Up

-- ---- network ----
CREATE TABLE stations (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) > 0),
  location   text NOT NULL DEFAULT '',
  notes      text NOT NULL DEFAULT '',
  is_active  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, name)
);

CREATE TABLE generators (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  station_id   uuid NOT NULL,
  name         text NOT NULL CHECK (length(trim(name)) > 0),
  kva          numeric(10, 2) CHECK (kva IS NULL OR kva > 0),
  brand        text NOT NULL DEFAULT '',
  serial       text NOT NULL DEFAULT '',
  fuel_item_id uuid,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'maintenance', 'retired')),
  notes        text NOT NULL DEFAULT '',
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, station_id) REFERENCES stations (tenant_id, id),
  FOREIGN KEY (tenant_id, fuel_item_id) REFERENCES items (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, name)
);

CREATE TABLE boxes (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  station_id uuid NOT NULL,
  code       text NOT NULL CHECK (length(trim(code)) > 0),
  location   text NOT NULL DEFAULT '',
  notes      text NOT NULL DEFAULT '',
  is_active  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, station_id) REFERENCES stations (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, code)
);

ALTER TABLE subscriptions ADD COLUMN box_id uuid;
ALTER TABLE subscriptions ADD FOREIGN KEY (tenant_id, box_id) REFERENCES boxes (tenant_id, id);
CREATE INDEX subscriptions_box_idx ON subscriptions (tenant_id, box_id);

-- ---- generator operations ----
-- Engine hour-meter readings (one per generator per day).
CREATE TABLE generator_hours (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  generator_id uuid NOT NULL,
  log_date     date NOT NULL,
  hour_meter   numeric(12, 1) NOT NULL CHECK (hour_meter >= 0),
  notes        text NOT NULL DEFAULT '',
  created_by   uuid DEFAULT app_user(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, generator_id) REFERENCES generators (tenant_id, id),
  UNIQUE (tenant_id, generator_id, log_date)
);

CREATE TABLE outages (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  generator_id uuid NOT NULL,
  started_at   timestamptz NOT NULL,
  ended_at     timestamptz,
  reason       text NOT NULL DEFAULT '',
  created_by   uuid DEFAULT app_user(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, generator_id) REFERENCES generators (tenant_id, id),
  CHECK (ended_at IS NULL OR ended_at >= started_at)
);
CREATE INDEX outages_generator_idx ON outages (tenant_id, generator_id, started_at);

-- Fuel used can be charged to a generator. stock_docs is append-only, so the column is set at insert.
ALTER TABLE stock_docs ADD COLUMN generator_id uuid;
ALTER TABLE stock_docs ADD FOREIGN KEY (tenant_id, generator_id) REFERENCES generators (tenant_id, id);

-- ---- official tariff (e.g. Lebanon MoEW monthly price per kWh and fixed fees per amperage) ----
CREATE TABLE tariffs (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  period        text NOT NULL CHECK (period ~ '^\d{4}-(0[1-9]|1[0-2])$'),
  currency      char(3) NOT NULL REFERENCES currencies (code),
  price_per_kwh numeric(20, 4) NOT NULL CHECK (price_per_kwh >= 0),
  fixed_fees    jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(fixed_fees) = 'object'),
  notes         text NOT NULL DEFAULT '',
  applied_at    timestamptz,
  applied_by    uuid,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, company_id, period)
);

-- ---- taxes ----
CREATE TABLE tax_rates (
  id                  uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id           uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id          uuid NOT NULL,
  name                text NOT NULL CHECK (length(trim(name)) > 0),
  rate                numeric(7, 4) NOT NULL CHECK (rate >= 0 AND rate <= 100),
  applies_to          text NOT NULL DEFAULT 'both' CHECK (applies_to IN ('sales', 'purchases', 'both')),
  account_id          uuid,          -- tax collected on sales (payable)
  purchase_account_id uuid,          -- tax paid on purchases (recoverable)
  is_default          boolean NOT NULL DEFAULT false,
  is_active           boolean NOT NULL DEFAULT true,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, purchase_account_id) REFERENCES accounts (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, name),
  CHECK (applies_to = 'purchases' OR account_id IS NOT NULL),
  CHECK (applies_to = 'sales' OR purchase_account_id IS NOT NULL)
);

ALTER TABLE plans ADD COLUMN tax_rate_id uuid;
ALTER TABLE plans ADD FOREIGN KEY (tenant_id, tax_rate_id) REFERENCES tax_rates (tenant_id, id);

ALTER TABLE invoice_lines ADD COLUMN tax_rate_id uuid;
ALTER TABLE invoice_lines ADD COLUMN tax_amount numeric(20, 4) NOT NULL DEFAULT 0 CHECK (tax_amount >= 0);
ALTER TABLE invoice_lines ADD FOREIGN KEY (tenant_id, tax_rate_id) REFERENCES tax_rates (tenant_id, id);
ALTER TABLE bill_lines ADD COLUMN tax_rate_id uuid;
ALTER TABLE bill_lines ADD COLUMN tax_amount numeric(20, 4) NOT NULL DEFAULT 0 CHECK (tax_amount >= 0);
ALTER TABLE bill_lines ADD FOREIGN KEY (tenant_id, tax_rate_id) REFERENCES tax_rates (tenant_id, id);

-- ---- bills shown in a second currency (snapshot of the official rate at issue) ----
ALTER TABLE companies ADD COLUMN show_local_currency_on_bills boolean NOT NULL DEFAULT false;
ALTER TABLE invoices ADD COLUMN local_currency char(3) REFERENCES currencies (code);
ALTER TABLE invoices ADD COLUMN local_rate numeric(28, 10);
ALTER TABLE invoices ADD COLUMN local_currency_total numeric(20, 4);

-- Lebanese companies (second currency different from base) show LBP totals by default.
UPDATE companies SET show_local_currency_on_bills = true WHERE local_currency <> base_currency;

-- Default VAT rates for existing companies (created inactive-by-default on plans: not applied to any plan).
INSERT INTO tax_rates (tenant_id, company_id, name, rate, applies_to, account_id, purchase_account_id, is_default)
SELECT c.tenant_id, c.id, 'VAT 11%', 11, 'both', pay.id, rec.id, true
FROM companies c
JOIN accounts pay ON pay.company_id = c.id AND pay.code = '4427'
JOIN accounts rec ON rec.company_id = c.id AND rec.code = '4426'
ON CONFLICT DO NOTHING;
INSERT INTO tax_rates (tenant_id, company_id, name, rate, applies_to, account_id, purchase_account_id, is_default)
SELECT c.tenant_id, c.id, 'VAT', 10, 'both', pay.id, pay.id, true
FROM companies c
JOIN accounts pay ON pay.company_id = c.id AND pay.code = '2200'
WHERE NOT EXISTS (SELECT 1 FROM tax_rates t WHERE t.company_id = c.id)
ON CONFLICT DO NOTHING;

-- ---- triggers, RLS, grants ----
CREATE TRIGGER stations_touch BEFORE UPDATE ON stations FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER generators_touch BEFORE UPDATE ON generators FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER boxes_touch BEFORE UPDATE ON boxes FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER generator_hours_touch BEFORE UPDATE ON generator_hours FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER outages_touch BEFORE UPDATE ON outages FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER tariffs_touch BEFORE UPDATE ON tariffs FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER tax_rates_touch BEFORE UPDATE ON tax_rates FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

CREATE TRIGGER stations_audit AFTER INSERT OR UPDATE OR DELETE ON stations FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER generators_audit AFTER INSERT OR UPDATE OR DELETE ON generators FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER boxes_audit AFTER INSERT OR UPDATE OR DELETE ON boxes FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER generator_hours_audit AFTER INSERT OR UPDATE OR DELETE ON generator_hours FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER outages_audit AFTER INSERT OR UPDATE OR DELETE ON outages FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER tariffs_audit AFTER INSERT OR UPDATE OR DELETE ON tariffs FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER tax_rates_audit AFTER INSERT OR UPDATE OR DELETE ON tax_rates FOR EACH ROW EXECUTE FUNCTION audit_row();

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['stations', 'generators', 'boxes', 'generator_hours', 'outages', 'tariffs', 'tax_rates'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
  END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE invoices DROP COLUMN IF EXISTS local_currency, DROP COLUMN IF EXISTS local_rate, DROP COLUMN IF EXISTS local_currency_total;
ALTER TABLE companies DROP COLUMN IF EXISTS show_local_currency_on_bills;
ALTER TABLE bill_lines DROP COLUMN IF EXISTS tax_rate_id, DROP COLUMN IF EXISTS tax_amount;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS tax_rate_id, DROP COLUMN IF EXISTS tax_amount;
ALTER TABLE plans DROP COLUMN IF EXISTS tax_rate_id;
ALTER TABLE stock_docs DROP COLUMN IF EXISTS generator_id;
ALTER TABLE subscriptions DROP COLUMN IF EXISTS box_id;
DROP TABLE IF EXISTS tax_rates, tariffs, outages, generator_hours, boxes, generators, stations;
