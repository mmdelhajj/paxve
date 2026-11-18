-- Any currency, any language, developer API keys.

-- +goose Up

-- World currencies (ISO 4217). Shared reference data, read-only for tenants.
CREATE TABLE currencies (
  code     char(3) PRIMARY KEY CHECK (code ~ '^[A-Z]{3}$'),
  name     text NOT NULL,
  symbol   text NOT NULL,
  decimals smallint NOT NULL DEFAULT 2 CHECK (decimals BETWEEN 0 AND 4)
);

INSERT INTO currencies (code, name, symbol, decimals) VALUES
  ('USD', 'US Dollar', '$', 2),
  ('EUR', 'Euro', '€', 2),
  ('GBP', 'British Pound', '£', 2),
  ('LBP', 'Lebanese Pound', 'ل.ل', 0),
  ('SYP', 'Syrian Pound', 'ل.س', 0),
  ('IQD', 'Iraqi Dinar', 'ع.د', 0),
  ('JOD', 'Jordanian Dinar', 'د.أ', 3),
  ('EGP', 'Egyptian Pound', 'ج.م', 2),
  ('SAR', 'Saudi Riyal', 'ر.س', 2),
  ('AED', 'UAE Dirham', 'د.إ', 2),
  ('QAR', 'Qatari Riyal', 'ر.ق', 2),
  ('KWD', 'Kuwaiti Dinar', 'د.ك', 3),
  ('BHD', 'Bahraini Dinar', 'د.ب', 3),
  ('OMR', 'Omani Rial', 'ر.ع', 3),
  ('YER', 'Yemeni Rial', 'ر.ي', 0),
  ('LYD', 'Libyan Dinar', 'ل.د', 3),
  ('TND', 'Tunisian Dinar', 'د.ت', 3),
  ('DZD', 'Algerian Dinar', 'د.ج', 2),
  ('MAD', 'Moroccan Dirham', 'د.م', 2),
  ('SDG', 'Sudanese Pound', 'ج.س', 2),
  ('TRY', 'Turkish Lira', '₺', 2),
  ('IRR', 'Iranian Rial', '﷼', 0),
  ('CHF', 'Swiss Franc', 'CHF', 2),
  ('CAD', 'Canadian Dollar', 'C$', 2),
  ('AUD', 'Australian Dollar', 'A$', 2),
  ('NZD', 'New Zealand Dollar', 'NZ$', 2),
  ('JPY', 'Japanese Yen', '¥', 0),
  ('CNY', 'Chinese Yuan', '¥', 2),
  ('HKD', 'Hong Kong Dollar', 'HK$', 2),
  ('SGD', 'Singapore Dollar', 'S$', 2),
  ('INR', 'Indian Rupee', '₹', 2),
  ('PKR', 'Pakistani Rupee', '₨', 2),
  ('BDT', 'Bangladeshi Taka', '৳', 2),
  ('IDR', 'Indonesian Rupiah', 'Rp', 0),
  ('MYR', 'Malaysian Ringgit', 'RM', 2),
  ('PHP', 'Philippine Peso', '₱', 2),
  ('THB', 'Thai Baht', '฿', 2),
  ('KRW', 'South Korean Won', '₩', 0),
  ('RUB', 'Russian Ruble', '₽', 2),
  ('UAH', 'Ukrainian Hryvnia', '₴', 2),
  ('PLN', 'Polish Zloty', 'zł', 2),
  ('CZK', 'Czech Koruna', 'Kč', 2),
  ('HUF', 'Hungarian Forint', 'Ft', 2),
  ('RON', 'Romanian Leu', 'lei', 2),
  ('SEK', 'Swedish Krona', 'kr', 2),
  ('NOK', 'Norwegian Krone', 'kr', 2),
  ('DKK', 'Danish Krone', 'kr', 2),
  ('BRL', 'Brazilian Real', 'R$', 2),
  ('MXN', 'Mexican Peso', 'MX$', 2),
  ('ARS', 'Argentine Peso', 'AR$', 2),
  ('COP', 'Colombian Peso', 'COL$', 2),
  ('CLP', 'Chilean Peso', 'CLP$', 0),
  ('ZAR', 'South African Rand', 'R', 2),
  ('NGN', 'Nigerian Naira', '₦', 2),
  ('KES', 'Kenyan Shilling', 'KSh', 2),
  ('GHS', 'Ghanaian Cedi', '₵', 2),
  ('XOF', 'West African CFA Franc', 'CFA', 0),
  ('XAF', 'Central African CFA Franc', 'FCFA', 0),
  ('ETB', 'Ethiopian Birr', 'Br', 2),
  ('AMD', 'Armenian Dram', '֏', 2),
  ('GEL', 'Georgian Lari', '₾', 2),
  ('AZN', 'Azerbaijani Manat', '₼', 2),
  ('KZT', 'Kazakhstani Tenge', '₸', 2),
  ('XAU', 'Gold (troy ounce)', 'XAU', 4)
ON CONFLICT DO NOTHING;

GRANT SELECT ON currencies TO paxve_app;

ALTER TABLE companies
  ADD CONSTRAINT companies_base_currency_fk FOREIGN KEY (base_currency) REFERENCES currencies (code),
  ADD CONSTRAINT companies_local_currency_fk FOREIGN KEY (local_currency) REFERENCES currencies (code);

-- Exchange rates per company: rate = units of `currency` for 1 unit of the company base currency
-- (base USD, currency LBP, rate 89500 means 1 USD = 89,500 LBP).
CREATE TABLE exchange_rates (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  currency   char(3) NOT NULL REFERENCES currencies (code),
  rate_date  date NOT NULL,
  kind       text NOT NULL DEFAULT 'official' CHECK (kind IN ('official', 'buy', 'sell')),
  rate       numeric(28, 10) NOT NULL CHECK (rate > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, company_id, currency, kind, rate_date)
);
CREATE INDEX exchange_rates_lookup_idx ON exchange_rates (tenant_id, company_id, currency, kind, rate_date DESC);
CREATE TRIGGER exchange_rates_touch BEFORE UPDATE ON exchange_rates FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- Languages: any ISO language code, not only en/ar.
ALTER TABLE users DROP CONSTRAINT users_locale_check;
ALTER TABLE users ADD CONSTRAINT users_locale_check CHECK (locale ~ '^[a-z]{2}(-[A-Z]{2})?$');

-- Developer API keys. Only a SHA-256 hash of the key is stored.
CREATE TABLE api_keys (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  name         text NOT NULL CHECK (length(trim(name)) > 0),
  prefix       text NOT NULL UNIQUE,
  key_hash     bytea NOT NULL,
  role         text NOT NULL CHECK (role IN ('admin', 'accountant', 'data_entry', 'read_only')),
  created_by   uuid NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  revoked_at   timestamptz,
  FOREIGN KEY (tenant_id, created_by) REFERENCES users (tenant_id, id)
);
CREATE TRIGGER api_keys_touch BEFORE UPDATE ON api_keys FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- +goose StatementBegin
CREATE FUNCTION api_key_lookup(p_prefix text)
RETURNS TABLE (id uuid, tenant_id uuid, name text, key_hash bytea, role text, created_by uuid, active boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT k.id, k.tenant_id, k.name, k.key_hash, k.role, k.created_by,
         (k.revoked_at IS NULL AND t.is_active AND u.is_active)
  FROM api_keys k
  JOIN tenants t ON t.id = k.tenant_id
  JOIN users u ON u.id = k.created_by
  WHERE k.prefix = p_prefix
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION api_key_lookup(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION api_key_lookup(text) TO paxve_app;

-- Audit: record which API key made a change, and ignore last_used_at noise.
ALTER TABLE audit_log ADD COLUMN api_key_id uuid;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_row() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_old    jsonb;
  v_new    jsonb;
  v_tenant uuid;
  v_id     uuid;
BEGIN
  IF TG_OP <> 'INSERT' THEN v_old := to_jsonb(OLD) - 'password_hash' - 'key_hash' - 'updated_at' - 'last_used_at'; END IF;
  IF TG_OP <> 'DELETE' THEN v_new := to_jsonb(NEW) - 'password_hash' - 'key_hash' - 'updated_at' - 'last_used_at'; END IF;

  IF TG_OP = 'UPDATE' THEN
    IF (to_jsonb(OLD) ->> 'password_hash') IS DISTINCT FROM (to_jsonb(NEW) ->> 'password_hash') THEN
      v_new := v_new || '{"password_changed": true}'::jsonb;
    END IF;
    IF v_old = v_new THEN
      RETURN NEW;
    END IF;
  END IF;

  v_tenant := coalesce(v_new ->> 'tenant_id', v_old ->> 'tenant_id', v_new ->> 'id', v_old ->> 'id')::uuid;
  v_id     := coalesce(v_new ->> 'id', v_old ->> 'id')::uuid;

  INSERT INTO audit_log (tenant_id, user_id, api_key_id, action, entity, entity_id, before, after, ip)
  VALUES (v_tenant, app_user(), nullif(current_setting('app.api_key_id', true), '')::uuid,
          lower(TG_OP), TG_TABLE_NAME, v_id, v_old, v_new,
          nullif(current_setting('app.ip', true), '')::inet);
  RETURN coalesce(NEW, OLD);
END
$$;
-- +goose StatementEnd

CREATE TRIGGER exchange_rates_audit AFTER INSERT OR UPDATE OR DELETE ON exchange_rates FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER api_keys_audit AFTER INSERT OR UPDATE OR DELETE ON api_keys FOR EACH ROW EXECUTE FUNCTION audit_row();

ALTER TABLE exchange_rates ENABLE ROW LEVEL SECURITY;
ALTER TABLE exchange_rates FORCE ROW LEVEL SECURITY;
ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exchange_rates
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON api_keys
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());

GRANT SELECT, INSERT, UPDATE ON exchange_rates, api_keys TO paxve_app;

-- +goose Down
DROP TABLE IF EXISTS api_keys;
DROP FUNCTION IF EXISTS api_key_lookup(text);
DROP TABLE IF EXISTS exchange_rates;
ALTER TABLE audit_log DROP COLUMN IF EXISTS api_key_id;
ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_base_currency_fk, DROP CONSTRAINT IF EXISTS companies_local_currency_fk;
DROP TABLE IF EXISTS currencies;
