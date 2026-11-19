-- Platform owner console: editable plans, paid-until / renewals, payments received from organisations,
-- lifecycle (trial → active → grace → expired → blocked), reminders, owner action log, renewal requests.
-- +goose Up

CREATE TABLE platform_plans (
  code           text PRIMARY KEY CHECK (code ~ '^[a-z][a-z0-9_]{1,30}$'),
  name_en        text NOT NULL CHECK (length(trim(name_en)) > 0),
  name_ar        text NOT NULL DEFAULT '',
  name_fr        text NOT NULL DEFAULT '',
  price_monthly  numeric(12, 2) NOT NULL DEFAULT 0 CHECK (price_monthly >= 0),
  price_yearly   numeric(12, 2) NOT NULL DEFAULT 0 CHECK (price_yearly >= 0),
  currency       char(3) NOT NULL DEFAULT 'USD',
  max_subscribers int CHECK (max_subscribers IS NULL OR max_subscribers >= 0),
  max_users      int CHECK (max_users IS NULL OR max_users >= 0),
  max_companies  int CHECK (max_companies IS NULL OR max_companies >= 0),
  apps           text[],               -- NULL = every app
  is_public      boolean NOT NULL DEFAULT true,
  is_active      boolean NOT NULL DEFAULT true,
  sort           int NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER platform_plans_touch BEFORE UPDATE ON platform_plans FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
-- the same limits and prices as before (trial 50, starter 300, business 1500, pro unlimited)
INSERT INTO platform_plans (code, name_en, name_ar, name_fr, price_monthly, price_yearly, max_subscribers, max_users, max_companies, sort, is_public) VALUES
  ('trial',    'Trial',    'تجربة مجانية', 'Essai',      0,   0,    50,   NULL, NULL, 0, false),
  ('starter',  'Starter',  'أساسي',        'Démarrage',  25,  250,  300,  NULL, NULL, 1, true),
  ('business', 'Business', 'أعمال',        'Business',   59,  590,  1500, NULL, NULL, 2, true),
  ('pro',      'Pro',      'احترافي',      'Pro',        129, 1290, NULL, NULL, NULL, 3, true);
GRANT SELECT ON platform_plans TO paxve_app;

ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_plan_check;
ALTER TABLE tenants ADD CONSTRAINT tenants_plan_fkey FOREIGN KEY (plan) REFERENCES platform_plans (code) ON UPDATE CASCADE;
ALTER TABLE tenants
  ADD COLUMN paid_until     date,
  ADD COLUMN billing_cycle  text NOT NULL DEFAULT 'monthly' CHECK (billing_cycle IN ('monthly', 'yearly')),
  ADD COLUMN price_override numeric(12, 2) CHECK (price_override IS NULL OR price_override >= 0),
  ADD COLUMN price_currency char(3),
  ADD COLUMN grace_days     int NOT NULL DEFAULT 7 CHECK (grace_days BETWEEN 0 AND 90),
  ADD COLUMN blocked_at     timestamptz,
  ADD COLUMN blocked_reason text NOT NULL DEFAULT '',
  ADD COLUMN notes          text NOT NULL DEFAULT '',
  ADD COLUMN contact_phone  text NOT NULL DEFAULT '',
  ADD COLUMN contact_email  text NOT NULL DEFAULT '',
  ADD COLUMN max_users      int CHECK (max_users IS NULL OR max_users >= 0),
  ADD COLUMN max_companies  int CHECK (max_companies IS NULL OR max_companies >= 0),
  ADD COLUMN last_state     text NOT NULL DEFAULT '';
-- organisations switched off before: blocked, with the reason they had
UPDATE tenants SET blocked_at = updated_at, blocked_reason = suspended_reason WHERE NOT is_active;
UPDATE tenants t SET contact_email = coalesce((SELECT min(u.email) FROM users u WHERE u.tenant_id = t.id AND u.role = 'admin'), '');

-- Where an organisation stands. is_active = false is the block (login refused); a paid plan without a
-- paid-until date (organisations set up by hand before this) counts as active.
-- +goose StatementBegin
CREATE FUNCTION tenant_state(t tenants) RETURNS text
LANGUAGE sql STABLE AS $$
  SELECT CASE
    WHEN NOT t.is_active THEN 'blocked'
    WHEN t.plan = 'trial' THEN CASE WHEN t.trial_ends_at IS NULL OR t.trial_ends_at > now() THEN 'trial' ELSE 'expired' END
    WHEN t.paid_until IS NULL OR t.paid_until >= current_date THEN 'active'
    WHEN t.paid_until + t.grace_days >= current_date THEN 'grace'
    ELSE 'expired'
  END
$$;
-- +goose StatementEnd
GRANT EXECUTE ON FUNCTION tenant_state(tenants) TO paxve_app;
UPDATE tenants t SET last_state = tenant_state(t);

-- money received from organisations for their Paxve plan
CREATE TABLE platform_payments (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL REFERENCES tenants (id),
  amount            numeric(12, 2) NOT NULL CHECK (amount >= 0),
  currency          char(3) NOT NULL DEFAULT 'USD',
  method            text NOT NULL CHECK (method IN ('cash', 'whish', 'omt', 'bank', 'card', 'other')),
  reference         text NOT NULL DEFAULT '',
  plan              text NOT NULL,
  cycle             text NOT NULL DEFAULT 'monthly',
  period_from       date NOT NULL,
  period_to         date NOT NULL CHECK (period_to >= period_from),
  note              text NOT NULL DEFAULT '',
  recorded_by_email text NOT NULL DEFAULT '',
  created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX platform_payments_tenant_idx ON platform_payments (tenant_id, created_at DESC);
ALTER TABLE platform_payments ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform_payments FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON platform_payments USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
GRANT SELECT ON platform_payments TO paxve_app; -- an organisation sees its own payments; only the owner records them

-- every action of the platform owner, and lifecycle changes found by the daily worker
CREATE TABLE platform_events (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id   uuid REFERENCES tenants (id),
  at          timestamptz NOT NULL DEFAULT now(),
  actor_email text NOT NULL DEFAULT '',
  action      text NOT NULL,
  detail      jsonb NOT NULL DEFAULT '{}'::jsonb,
  ip          inet
);
CREATE INDEX platform_events_tenant_idx ON platform_events (tenant_id, id DESC);
CREATE TRIGGER platform_events_no_update BEFORE UPDATE OR DELETE ON platform_events FOR EACH ROW EXECUTE FUNCTION forbid_change();

-- renewal / upgrade requests sent by organisations from "My Paxve subscription"
CREATE TABLE platform_requests (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  kind          text NOT NULL CHECK (kind IN ('renew', 'upgrade')),
  plan          text NOT NULL REFERENCES platform_plans (code) ON UPDATE CASCADE,
  cycle         text NOT NULL DEFAULT 'monthly' CHECK (cycle IN ('monthly', 'yearly')),
  method        text NOT NULL DEFAULT 'other' CHECK (method IN ('cash', 'whish', 'omt', 'bank', 'card', 'other')),
  reference     text NOT NULL DEFAULT '' CHECK (length(reference) <= 120),
  note          text NOT NULL DEFAULT '' CHECK (length(note) <= 1000),
  status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done', 'rejected')),
  created_by    uuid DEFAULT app_user(),
  created_at    timestamptz NOT NULL DEFAULT now(),
  handled_at    timestamptz,
  handled_by    text NOT NULL DEFAULT '',
  answer        text NOT NULL DEFAULT ''
);
CREATE INDEX platform_requests_open_idx ON platform_requests (status, created_at DESC);
ALTER TABLE platform_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON platform_requests USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
GRANT SELECT, INSERT ON platform_requests TO paxve_app;

-- reminders already sent (one per kind and end date)
CREATE TABLE platform_reminders (
  tenant_id uuid NOT NULL REFERENCES tenants (id),
  kind      text NOT NULL CHECK (kind IN ('d7', 'd3', 'd1', 'expired')),
  for_date  date NOT NULL,
  channels  text NOT NULL DEFAULT '',
  sent_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, kind, for_date)
);

-- the organisation may keep its own contact phone up to date (reminders go there)
GRANT UPDATE (contact_phone) ON tenants TO paxve_app;

-- +goose Down
REVOKE UPDATE (contact_phone) ON tenants FROM paxve_app;
DROP TABLE IF EXISTS platform_reminders;
DROP TABLE IF EXISTS platform_requests;
DROP TABLE IF EXISTS platform_events;
DROP TABLE IF EXISTS platform_payments;
DROP FUNCTION IF EXISTS tenant_state(tenants);
ALTER TABLE tenants DROP COLUMN IF EXISTS last_state, DROP COLUMN IF EXISTS max_companies, DROP COLUMN IF EXISTS max_users,
  DROP COLUMN IF EXISTS contact_email, DROP COLUMN IF EXISTS contact_phone, DROP COLUMN IF EXISTS notes,
  DROP COLUMN IF EXISTS blocked_reason, DROP COLUMN IF EXISTS blocked_at, DROP COLUMN IF EXISTS grace_days,
  DROP COLUMN IF EXISTS price_currency, DROP COLUMN IF EXISTS price_override, DROP COLUMN IF EXISTS billing_cycle,
  DROP COLUMN IF EXISTS paid_until;
ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_plan_fkey;
DELETE FROM platform_plans WHERE code NOT IN ('trial', 'starter', 'business', 'pro');
ALTER TABLE tenants ADD CONSTRAINT tenants_plan_check CHECK (plan IN ('trial', 'starter', 'business', 'pro'));
DROP TABLE IF EXISTS platform_plans;
