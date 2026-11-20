-- Phase 5: smart features (app "ai").
--   * alerts center: deterministic checks against each company's own history (z-score / thresholds): generator
--     diesel per running hour, cashier discounts, cash box shortages at shift close, voids / credit notes per user,
--     payments taken outside working hours, stock count losses, collector collections dropping, duplicate supplier
--     bills (and the fleet fuel flags of phase 3); acknowledge / resolve / dismiss, a daily digest to the owner.
--   * late-payer / churn risk per customer (score with the factors that explain it), feeding the collector route.
--   * customer coordinates (captured from the collector app's GPS) for the collector route map.
--   * AI assistant and supplier-bill reading (Claude): key-gated, metered per organisation by plan.
--
-- No money moves here: forecasts, risk and reorder suggestions are read-only; purchase requests and bills created
-- from these screens go through the normal documents.

-- +goose Up

-- ---------- app "ai": on for new companies and switched on for existing ones (it only reads) ----------
ALTER TABLE companies DROP CONSTRAINT companies_apps_known;
ALTER TABLE companies ADD CONSTRAINT companies_apps_known
  CHECK (apps <@ ARRAY['gen','acc','col','msg','stk','pur','net','sat','pos','hr','crm','svc','mfg','prj','flt','web','mkt','ai']::text[]);
ALTER TABLE companies ALTER COLUMN apps SET DEFAULT '{gen,acc,col,msg,stk,pur,net,sat,pos,hr,crm,svc,mfg,prj,flt,web,mkt,ai}';
UPDATE companies SET apps = array_append(apps, 'ai') WHERE NOT ('ai' = ANY (apps));
-- plans with an explicit app list get the new app too (it is part of every plan unless the owner removes it)
UPDATE platform_plans SET apps = array_append(apps, 'ai') WHERE apps IS NOT NULL AND NOT ('ai' = ANY (apps));

-- AI requests per organisation per month (NULL = unlimited)
ALTER TABLE platform_plans ADD COLUMN ai_monthly_limit int CHECK (ai_monthly_limit IS NULL OR ai_monthly_limit >= 0);
UPDATE platform_plans SET ai_monthly_limit = CASE code WHEN 'trial' THEN 30 WHEN 'starter' THEN 300 WHEN 'business' THEN 1500 ELSE NULL END;

ALTER TABLE user_notifications DROP CONSTRAINT IF EXISTS user_notifications_kind_check;
ALTER TABLE user_notifications ADD CONSTRAINT user_notifications_kind_check
  CHECK (kind IN ('mention', 'activity', 'activity_due', 'repair', 'system', 'task', 'service', 'sla', 'automation', 'fleet',
                  'shop', 'recruit', 'appraisal', 'esign', 'campaign', 'smart'));

-- ---------- customer coordinates ----------
ALTER TABLE customers ADD COLUMN lat numeric(9, 6) CHECK (lat IS NULL OR lat BETWEEN -90 AND 90),
                      ADD COLUMN lng numeric(9, 6) CHECK (lng IS NULL OR lng BETWEEN -180 AND 180),
                      ADD COLUMN geo_at timestamptz,
                      ADD COLUMN geo_source text NOT NULL DEFAULT '' CHECK (geo_source IN ('', 'gps', 'manual'));
ALTER TABLE customers ADD CONSTRAINT customers_geo_pair CHECK ((lat IS NULL) = (lng IS NULL));

-- smart_to_base: an amount of currency cur in the company's base currency at the latest official rate (NULL when no rate)
-- +goose StatementBegin
CREATE FUNCTION smart_to_base(p_company uuid, p_cur text, p_amount numeric) RETURNS numeric
LANGUAGE sql STABLE AS $$
  SELECT CASE WHEN p_amount IS NULL THEN NULL
              WHEN p_cur = (SELECT base_currency::text FROM companies WHERE id = p_company) THEN p_amount
              ELSE p_amount / nullif((SELECT x.rate FROM exchange_rates x WHERE x.company_id = p_company AND x.currency = p_cur
                                      AND x.kind = 'official' ORDER BY x.rate_date DESC LIMIT 1), 0) END
$$;
-- +goose StatementEnd
GRANT EXECUTE ON FUNCTION smart_to_base(uuid, text, numeric) TO paxve_app;

-- ---------- settings per company ----------
CREATE TABLE smart_settings (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  params         jsonb NOT NULL DEFAULT '{}'::jsonb,   -- thresholds (see smartDefaults in the code)
  digest_enabled boolean NOT NULL DEFAULT true,
  digest_hour    int NOT NULL DEFAULT 8 CHECK (digest_hour BETWEEN 0 AND 23),
  digest_phone   text NOT NULL DEFAULT '' CHECK (length(digest_phone) <= 40),
  last_scan_at   timestamptz,
  last_risk_at   timestamptz,
  last_digest_on date,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id)
);

-- ---------- alerts ----------
CREATE TABLE smart_alerts (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  kind          text NOT NULL CHECK (kind IN ('diesel', 'discount', 'cash_diff', 'cash_repeat', 'voids', 'off_hours', 'count_loss',
                                              'collector_drop', 'dup_bill', 'fleet_fuel')),
  severity      text NOT NULL DEFAULT 'medium' CHECK (severity IN ('low', 'medium', 'high')),
  fingerprint   text NOT NULL CHECK (length(fingerprint) BETWEEN 1 AND 300),
  subject_type  text NOT NULL DEFAULT '' CHECK (length(subject_type) <= 40),
  subject_id    uuid,
  subject_label text NOT NULL DEFAULT '' CHECK (length(subject_label) <= 300),
  period        text NOT NULL DEFAULT '' CHECK (length(period) <= 60),
  value         numeric(24, 6),
  expected      numeric(24, 6),
  score         numeric(12, 4),
  detail        jsonb NOT NULL DEFAULT '{}'::jsonb,
  status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'ack', 'resolved', 'dismissed')),
  note          text NOT NULL DEFAULT '' CHECK (length(note) <= 1000),
  acted_by      uuid REFERENCES users (id),
  acted_at      timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, fingerprint)
);
CREATE INDEX smart_alerts_open_idx ON smart_alerts (company_id, status, created_at DESC);

-- ---------- late-payer / churn risk ----------
CREATE TABLE customer_risk (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  customer_id    uuid NOT NULL,
  score          int NOT NULL CHECK (score BETWEEN 0 AND 100),
  band           text NOT NULL CHECK (band IN ('low', 'medium', 'high')),
  late_score     int NOT NULL CHECK (late_score BETWEEN 0 AND 100),
  churn_score    int NOT NULL CHECK (churn_score BETWEEN 0 AND 100),
  avg_days_late  numeric(10, 2),
  overdue_base   numeric(20, 4) NOT NULL DEFAULT 0,
  factors        jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{code, value, points}]
  computed_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT customer_risk_customer_id_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, customer_id)
);
CREATE INDEX customer_risk_band_idx ON customer_risk (company_id, score DESC);

-- ---------- AI requests (assistant questions and bill readings): the answer, and the meter ----------
CREATE TABLE ai_requests (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  user_id       uuid DEFAULT app_user() REFERENCES users (id),
  kind          text NOT NULL CHECK (kind IN ('chat', 'ocr')),
  status        text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done', 'error')),
  question      text NOT NULL DEFAULT '' CHECK (length(question) <= 4000),
  answer        text NOT NULL DEFAULT '' CHECK (length(answer) <= 40000),
  tools         text[] NOT NULL DEFAULT '{}',
  error         text NOT NULL DEFAULT '' CHECK (length(error) <= 400),
  model         text NOT NULL DEFAULT '',
  input_tokens  bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL DEFAULT now(),
  finished_at   timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX ai_requests_month_idx ON ai_requests (tenant_id, created_at);

-- ---------- scanned supplier bills ----------
CREATE TABLE ocr_scans (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  filename    text NOT NULL CHECK (length(trim(filename)) BETWEEN 1 AND 200),
  mime        text NOT NULL CHECK (mime IN ('image/jpeg', 'image/png', 'image/webp', 'image/gif', 'application/pdf')),
  size        int NOT NULL CHECK (size > 0 AND size <= 10485760),
  data        bytea NOT NULL,
  status      text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'reading', 'read', 'error', 'billed')),
  error       text NOT NULL DEFAULT '' CHECK (length(error) <= 400),
  extracted   jsonb,
  draft       jsonb,
  request_id  uuid,
  bill_id     uuid,
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT ocr_scans_bill_id_same_company FOREIGN KEY (company_id, bill_id) REFERENCES bills (company_id, id),
  CONSTRAINT ocr_scans_request_id_same_company FOREIGN KEY (company_id, request_id) REFERENCES ai_requests (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX ocr_scans_company_idx ON ocr_scans (company_id, created_at DESC);

-- ---------- row security, grants and the audit trail ----------
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['smart_settings', 'smart_alerts', 'customer_risk', 'ai_requests', 'ocr_scans'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
    IF t IN ('smart_settings', 'smart_alerts') THEN
      EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    END IF;
  END LOOP;
END
$$;
-- +goose StatementEnd
GRANT DELETE ON ocr_scans, customer_risk TO paxve_app;

-- +goose Down
DROP TABLE IF EXISTS ocr_scans, ai_requests, customer_risk, smart_alerts, smart_settings CASCADE;
DROP FUNCTION IF EXISTS smart_to_base(uuid, text, numeric);
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_geo_pair;
ALTER TABLE customers DROP COLUMN IF EXISTS lat, DROP COLUMN IF EXISTS lng, DROP COLUMN IF EXISTS geo_at, DROP COLUMN IF EXISTS geo_source;
ALTER TABLE platform_plans DROP COLUMN IF EXISTS ai_monthly_limit;
UPDATE platform_plans SET apps = array_remove(apps, 'ai') WHERE apps IS NOT NULL;
UPDATE companies SET apps = array_remove(apps, 'ai');
ALTER TABLE companies DROP CONSTRAINT companies_apps_known;
ALTER TABLE companies ADD CONSTRAINT companies_apps_known
  CHECK (apps <@ ARRAY['gen','acc','col','msg','stk','pur','net','sat','pos','hr','crm','svc','mfg','prj','flt','web','mkt']::text[]);
ALTER TABLE companies ALTER COLUMN apps SET DEFAULT '{gen,acc,col,msg,stk,pur,net,sat,pos,hr,crm,svc,mfg,prj,flt,web,mkt}';
