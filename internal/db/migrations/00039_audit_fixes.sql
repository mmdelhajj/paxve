-- +goose Up
-- Fixes from the live and technical audits of 2026-11-20.

-- B-M2: "today", report dates and current_date follow the company's time zone.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION tz_for_country(c text) RETURNS text LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE upper(coalesce(c, ''))
    WHEN 'LB' THEN 'Asia/Beirut' WHEN 'SA' THEN 'Asia/Riyadh' WHEN 'AE' THEN 'Asia/Dubai' WHEN 'KW' THEN 'Asia/Kuwait'
    WHEN 'QA' THEN 'Asia/Qatar' WHEN 'BH' THEN 'Asia/Bahrain' WHEN 'OM' THEN 'Asia/Muscat' WHEN 'JO' THEN 'Asia/Amman'
    WHEN 'SY' THEN 'Asia/Damascus' WHEN 'IQ' THEN 'Asia/Baghdad' WHEN 'EG' THEN 'Africa/Cairo' WHEN 'MA' THEN 'Africa/Casablanca'
    WHEN 'DZ' THEN 'Africa/Algiers' WHEN 'TN' THEN 'Africa/Tunis' WHEN 'TR' THEN 'Europe/Istanbul' WHEN 'FR' THEN 'Europe/Paris'
    WHEN 'GB' THEN 'Europe/London' WHEN 'US' THEN 'America/New_York'
    ELSE NULL END
$$;
-- +goose StatementEnd

ALTER TABLE companies ADD COLUMN timezone text NOT NULL DEFAULT 'Asia/Beirut'
  CHECK (timezone ~ '^[A-Za-z_]+(/[A-Za-z0-9_+-]+){1,2}$');
UPDATE companies SET timezone = coalesce(tz_for_country(country), 'Asia/Beirut');

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION companies_timezone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  -- a new company (or a new country) takes the country's zone unless a zone was chosen explicitly
  IF TG_OP = 'INSERT' THEN
    IF NEW.timezone = 'Asia/Beirut' AND tz_for_country(NEW.country) IS NOT NULL THEN
      NEW.timezone := tz_for_country(NEW.country);
    END IF;
  ELSIF NEW.country IS DISTINCT FROM OLD.country AND NEW.timezone = OLD.timezone AND tz_for_country(NEW.country) IS NOT NULL THEN
    NEW.timezone := tz_for_country(NEW.country);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_timezone_names WHERE name = NEW.timezone) THEN
    RAISE EXCEPTION 'unknown time zone %', NEW.timezone USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER companies_timezone BEFORE INSERT OR UPDATE OF timezone, country ON companies
  FOR EACH ROW EXECUTE FUNCTION companies_timezone();

-- B-M6: payroll posting date: auto (today when inside the month, else the month end) or always the month end
ALTER TABLE companies ADD COLUMN payroll_post_date text NOT NULL DEFAULT 'auto' CHECK (payroll_post_date IN ('auto', 'period_end'));

-- C-M3: each fingerprint device pushes to its own secret address (/iclock/k/<token>/...)
ALTER TABLE attendance_devices ADD COLUMN push_token text NOT NULL
  DEFAULT substr(replace(gen_random_uuid()::text || gen_random_uuid()::text, '-', ''), 1, 40);
CREATE UNIQUE INDEX attendance_devices_push_token_idx ON attendance_devices (push_token);

-- C-M2: a simulated (test-mode) payment is recorded as test_paid and never books a receipt
ALTER TABLE payment_intents DROP CONSTRAINT IF EXISTS payment_intents_status_check;
ALTER TABLE payment_intents ADD CONSTRAINT payment_intents_status_check
  CHECK (status IN ('pending', 'review', 'paid', 'failed', 'rejected', 'expired', 'test_paid'));

-- C-H1: platform owners must turn on two-step sign-in (7-day grace from their first sign-in after this rule)
ALTER TABLE users ADD COLUMN platform_mfa_deadline timestamptz;

-- C-L5: indexes for foreign keys that are scanned on voids, deletes and lookups
-- +goose StatementBegin
DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT * FROM (VALUES
      ('payment_tenders', 'payment_id'), ('payment_tenders', 'method_id'),
      ('invoices', 'journal_entry_id'), ('invoices', 'void_entry_id'),
      ('payments', 'journal_entry_id'), ('payments', 'void_entry_id'),
      ('invoice_lines', 'item_id'), ('invoice_lines', 'warehouse_id'), ('invoice_lines', 'order_line_id'),
      ('customers', 'area_id'), ('customers', 'price_list_id'),
      ('subscriptions', 'plan_id'),
      ('journal_entries', 'reversal_of'), ('journal_entries', 'reversed_by')) v(t, c)
  LOOP
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = r.t AND column_name = r.c) THEN
      EXECUTE format('CREATE INDEX IF NOT EXISTS %I ON %I (%I)', r.t || '_' || r.c || '_fk_idx', r.t, r.c);
    END IF;
  END LOOP;
END $$;
-- +goose StatementEnd

-- C-L7: the audit log is company-scoped in the database too (organisation-level rows have no company)
CREATE POLICY company_scope ON audit_log AS RESTRICTIVE FOR ALL
  USING (company_id IS NULL OR (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company()))
         AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))))
  WITH CHECK (company_id IS NULL OR (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company()))
         AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))));

-- retail: the Arabic / French names of the gift-card and loyalty accounts were stored garbled (wrong encoding)
UPDATE accounts SET name_i18n = jsonb_build_object('en', name, 'ar', 'بطاقات الهدايا والقسائم', 'fr', 'Cartes cadeaux et bons')
  WHERE name = 'Gift cards and vouchers' AND name_i18n->>'ar' IS DISTINCT FROM 'بطاقات الهدايا والقسائم';
UPDATE accounts SET name_i18n = jsonb_build_object('en', name, 'ar', 'نقاط الولاء المستحقة', 'fr', 'Points de fidélité à utiliser')
  WHERE name = 'Loyalty points to redeem' AND name_i18n->>'ar' IS DISTINCT FROM 'نقاط الولاء المستحقة';
UPDATE accounts SET name_i18n = jsonb_build_object('en', name, 'ar', 'مصاريف الولاء والعروض', 'fr', 'Fidélité et promotions')
  WHERE name = 'Loyalty and promotions' AND name_i18n->>'ar' IS DISTINCT FROM 'مصاريف الولاء والعروض';

-- +goose Down
UPDATE accounts SET name_i18n = name_i18n WHERE false;
DROP POLICY IF EXISTS company_scope ON audit_log;
ALTER TABLE users DROP COLUMN IF EXISTS platform_mfa_deadline;
ALTER TABLE payment_intents DROP CONSTRAINT IF EXISTS payment_intents_status_check;
UPDATE payment_intents SET status = 'failed' WHERE status = 'test_paid';
ALTER TABLE payment_intents ADD CONSTRAINT payment_intents_status_check
  CHECK (status IN ('pending', 'review', 'paid', 'failed', 'rejected', 'expired'));
DROP INDEX IF EXISTS attendance_devices_push_token_idx;
ALTER TABLE attendance_devices DROP COLUMN IF EXISTS push_token;
ALTER TABLE companies DROP COLUMN IF EXISTS payroll_post_date;
DROP TRIGGER IF EXISTS companies_timezone ON companies;
DROP FUNCTION IF EXISTS companies_timezone();
ALTER TABLE companies DROP COLUMN IF EXISTS timezone;
DROP FUNCTION IF EXISTS tz_for_country(text);
