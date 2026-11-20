-- Phase 4: growth and sales.
--   * online store per company (public shop at /#/shop/<slug>: catalog from the items with photos and variants, live
--     stock of one warehouse, prices of one price list, cart, delivery zones with fees, cash on delivery / pickup /
--     Whish / OMT, coupons from the promotions engine). An order becomes a normal sales order (source "online") and
--     follows the warehouse steps and the delivery board; the customer follows it with a public tracking link.
--   * marketing: mailing lists with opt-out, audiences by filters, campaigns over WhatsApp / SMS / e-mail through the
--     existing message queue (throttled, scheduled), tracked links, opt-out links, delivery stats.
--   * recruitment (job positions, public careers page with CV upload, applicant pipeline, interviews, offer → hire
--     creates the employee) and appraisals (cycles, goals, self + manager review, scores).
--   * e-sign: quotes, contracts, subscription agreements or uploaded PDFs signed through a public link (draw or type),
--     signers in order, IP / time / document hash, audit trail, reminders, a signed certificate PDF as attachment, and
--     a quote signed online can confirm itself into a sales order.
-- Money: nothing new is posted. Online orders, coupons and hires go through the existing documents (sales orders,
-- deliveries, invoices, payroll), so the ledger rules stay where they are.

-- +goose Up

-- ---------- apps: online store (web) and marketing (mkt); on for new companies, off for existing ones ----------
ALTER TABLE companies DROP CONSTRAINT companies_apps_known;
ALTER TABLE companies ADD CONSTRAINT companies_apps_known
  CHECK (apps <@ ARRAY['gen','acc','col','msg','stk','pur','net','sat','pos','hr','crm','svc','mfg','prj','flt','web','mkt']::text[]);
ALTER TABLE companies ALTER COLUMN apps SET DEFAULT '{gen,acc,col,msg,stk,pur,net,sat,pos,hr,crm,svc,mfg,prj,flt,web,mkt}';

-- one public address per company, shared by the shop and the careers page
ALTER TABLE companies ADD COLUMN public_slug text CHECK (public_slug IS NULL OR public_slug ~ '^[a-z0-9][a-z0-9-]{2,40}$');
CREATE UNIQUE INDEX companies_public_slug_idx ON companies (public_slug) WHERE public_slug IS NOT NULL;

ALTER TABLE user_notifications DROP CONSTRAINT IF EXISTS user_notifications_kind_check;
ALTER TABLE user_notifications ADD CONSTRAINT user_notifications_kind_check
  CHECK (kind IN ('mention', 'activity', 'activity_due', 'repair', 'system', 'task', 'service', 'sla', 'automation', 'fleet',
                  'shop', 'recruit', 'appraisal', 'esign', 'campaign'));

-- where a sales order came from
ALTER TABLE sales_orders ADD COLUMN source text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'online', 'esign'));
-- coupons used in the shop count like the till's
ALTER TABLE promotion_uses DROP CONSTRAINT IF EXISTS promotion_uses_source_check;
ALTER TABLE promotion_uses ADD CONSTRAINT promotion_uses_source_check CHECK (source IN ('pos', 'invoice', 'shop'));
-- a WhatsApp delivery receipt marks a sent message delivered
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_status_check;
ALTER TABLE messages ADD CONSTRAINT messages_status_check CHECK (status IN ('queued', 'sending', 'sent', 'delivered', 'failed'));
CREATE INDEX messages_provider_id_idx ON messages (provider_message_id) WHERE provider_message_id <> '';

-- ======================= online store =======================
ALTER TABLE items ADD COLUMN shop_visible boolean NOT NULL DEFAULT true,
  ADD COLUMN shop_description text NOT NULL DEFAULT '' CHECK (length(shop_description) <= 4000);

CREATE TABLE shop_settings (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id      uuid NOT NULL,
  enabled         boolean NOT NULL DEFAULT false,
  title           text NOT NULL DEFAULT '' CHECK (length(title) <= 120),
  tagline         text NOT NULL DEFAULT '' CHECK (length(tagline) <= 300),
  banner          text NOT NULL DEFAULT '' CHECK (banner = '' OR (banner LIKE 'data:image/%' AND length(banner) <= 700000)),
  color           text NOT NULL DEFAULT '#0f766e' CHECK (color ~ '^#[0-9a-fA-F]{6}$'),
  warehouse_id    uuid,
  price_list_id   uuid,
  currency        char(3) REFERENCES currencies (code),        -- NULL = the company's base currency
  show_local      boolean NOT NULL DEFAULT true,               -- also show prices in the local currency (LBP)
  payments        text[] NOT NULL DEFAULT '{cod,pickup}' CHECK (cardinality(payments) BETWEEN 1 AND 4 AND payments <@ ARRAY['cod','pickup','whish','omt']::text[]),
  pay_note        text NOT NULL DEFAULT '' CHECK (length(pay_note) <= 500),
  whatsapp        text NOT NULL DEFAULT '' CHECK (length(whatsapp) <= 30),
  min_order       numeric(20, 4) NOT NULL DEFAULT 0 CHECK (min_order >= 0),
  auto_confirm    boolean NOT NULL DEFAULT true,
  hide_out_of_stock boolean NOT NULL DEFAULT false,
  featured        uuid[] NOT NULL DEFAULT '{}' CHECK (cardinality(featured) <= 24),
  terms           text NOT NULL DEFAULT '' CHECK (length(terms) <= 4000),
  careers_enabled boolean NOT NULL DEFAULT false,
  careers_intro   text NOT NULL DEFAULT '' CHECK (length(careers_intro) <= 2000),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT shop_settings_warehouse_id_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id),
  CONSTRAINT shop_settings_price_list_id_same_company FOREIGN KEY (company_id, price_list_id) REFERENCES price_lists (company_id, id),
  UNIQUE (company_id),
  UNIQUE (company_id, id)
);

CREATE TABLE shop_zones (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 80),
  fee        numeric(20, 4) NOT NULL DEFAULT 0 CHECK (fee >= 0),   -- shop currency
  free_from  numeric(20, 4) CHECK (free_from IS NULL OR free_from >= 0),
  eta        text NOT NULL DEFAULT '' CHECK (length(eta) <= 60),
  seq        int NOT NULL DEFAULT 0,
  is_active  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id)
);

CREATE TABLE shop_orders (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  number         bigint NOT NULL,
  token          text NOT NULL CHECK (length(token) >= 32),
  sales_order_id uuid NOT NULL,
  customer_id    uuid NOT NULL,
  name           text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 160),
  phone          text NOT NULL CHECK (length(phone) BETWEEN 4 AND 40),
  email          text NOT NULL DEFAULT '' CHECK (length(email) <= 200),
  address        text NOT NULL DEFAULT '' CHECK (length(address) <= 500),
  zone_id        uuid,
  delivery       text NOT NULL DEFAULT 'delivery' CHECK (delivery IN ('delivery', 'pickup')),
  payment        text NOT NULL CHECK (payment IN ('cod', 'pickup', 'whish', 'omt')),
  coupon         text NOT NULL DEFAULT '' CHECK (length(coupon) <= 32),
  currency       char(3) NOT NULL REFERENCES currencies (code),
  subtotal       numeric(20, 4) NOT NULL DEFAULT 0,
  discount       numeric(20, 4) NOT NULL DEFAULT 0,
  fee            numeric(20, 4) NOT NULL DEFAULT 0,
  total          numeric(20, 4) NOT NULL DEFAULT 0,
  notes          text NOT NULL DEFAULT '' CHECK (length(notes) <= 1000),
  status         text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'confirmed', 'cancelled')),
  ip             text NOT NULL DEFAULT '',
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT shop_orders_sales_order_id_same_company FOREIGN KEY (company_id, sales_order_id) REFERENCES sales_orders (company_id, id),
  CONSTRAINT shop_orders_customer_id_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  CONSTRAINT shop_orders_zone_id_same_company FOREIGN KEY (company_id, zone_id) REFERENCES shop_zones (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number),
  UNIQUE (token)
);
CREATE INDEX shop_orders_company_idx ON shop_orders (company_id, created_at DESC);

-- ======================= marketing =======================
CREATE TABLE mkt_lists (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  name        text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
  description text NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
  is_active   boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id)
);

CREATE TABLE mkt_list_members (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  list_id     uuid NOT NULL,
  customer_id uuid,
  name        text NOT NULL DEFAULT '' CHECK (length(name) <= 160),
  phone       text NOT NULL DEFAULT '' CHECK (length(phone) <= 40),
  email       text NOT NULL DEFAULT '' CHECK (length(email) <= 200),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT mkt_list_members_list_id_same_company FOREIGN KEY (company_id, list_id) REFERENCES mkt_lists (company_id, id),
  CONSTRAINT mkt_list_members_customer_id_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  UNIQUE (company_id, id),
  CHECK (customer_id IS NOT NULL OR phone <> '' OR email <> '')
);
CREATE UNIQUE INDEX mkt_list_members_customer_idx ON mkt_list_members (list_id, customer_id) WHERE customer_id IS NOT NULL;
CREATE INDEX mkt_list_members_list_idx ON mkt_list_members (list_id);

-- people who asked not to receive marketing (per channel or all); matched by customer, phone or e-mail
CREATE TABLE mkt_optouts (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  channel     text NOT NULL DEFAULT 'all' CHECK (channel IN ('all', 'whatsapp', 'sms', 'email')),
  customer_id uuid,
  phone       text NOT NULL DEFAULT '' CHECK (length(phone) <= 40),
  email       text NOT NULL DEFAULT '' CHECK (length(email) <= 200),
  reason      text NOT NULL DEFAULT '' CHECK (length(reason) <= 300),
  source      text NOT NULL DEFAULT 'link' CHECK (source IN ('link', 'manual', 'reply')),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT mkt_optouts_customer_id_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  UNIQUE (company_id, id),
  CHECK (customer_id IS NOT NULL OR phone <> '' OR email <> '')
);
CREATE INDEX mkt_optouts_company_idx ON mkt_optouts (company_id);

CREATE TABLE mkt_campaigns (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id      uuid NOT NULL,
  number          bigint NOT NULL,
  name            text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 160),
  channel         text NOT NULL CHECK (channel IN ('whatsapp', 'sms', 'email')),
  audience        jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(audience) = 'object'),
  list_id         uuid,
  subject         text NOT NULL DEFAULT '' CHECK (length(subject) <= 200),
  body            text NOT NULL CHECK (length(trim(body)) BETWEEN 1 AND 4000),
  link_url        text NOT NULL DEFAULT '' CHECK (link_url = '' OR (link_url ~ '^https?://' AND length(link_url) <= 1000)),
  promotion_id    uuid,
  scheduled_at    timestamptz,
  throttle_per_min int NOT NULL DEFAULT 30 CHECK (throttle_per_min BETWEEN 1 AND 1000),
  status          text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'scheduled', 'sending', 'sent', 'cancelled')),
  recipients      int NOT NULL DEFAULT 0,
  skipped         int NOT NULL DEFAULT 0,
  sent_at         timestamptz,
  created_by      uuid DEFAULT app_user(),
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT mkt_campaigns_list_id_same_company FOREIGN KEY (company_id, list_id) REFERENCES mkt_lists (company_id, id),
  CONSTRAINT mkt_campaigns_promotion_id_same_company FOREIGN KEY (company_id, promotion_id) REFERENCES promotions (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
CREATE INDEX mkt_campaigns_due_idx ON mkt_campaigns (scheduled_at) WHERE status = 'scheduled';

CREATE TABLE mkt_recipients (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  campaign_id  uuid NOT NULL,
  customer_id  uuid,
  name         text NOT NULL DEFAULT '',
  phone        text NOT NULL DEFAULT '',
  email        text NOT NULL DEFAULT '',
  token        text NOT NULL CHECK (length(token) >= 32),
  message_id   uuid,
  status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'skipped')),
  skip_reason  text NOT NULL DEFAULT '',
  clicks       int NOT NULL DEFAULT 0,
  clicked_at   timestamptz,
  opted_out_at timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT mkt_recipients_campaign_id_same_company FOREIGN KEY (company_id, campaign_id) REFERENCES mkt_campaigns (company_id, id),
  CONSTRAINT mkt_recipients_customer_id_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  FOREIGN KEY (message_id) REFERENCES messages (id),
  UNIQUE (company_id, id),
  UNIQUE (token)
);
CREATE INDEX mkt_recipients_campaign_idx ON mkt_recipients (campaign_id);

-- ======================= recruitment =======================
CREATE TABLE job_positions (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  title         text NOT NULL CHECK (length(trim(title)) BETWEEN 1 AND 160),
  department    text NOT NULL DEFAULT '' CHECK (length(department) <= 80),
  location      text NOT NULL DEFAULT '' CHECK (length(location) <= 120),
  employment    text NOT NULL DEFAULT 'full_time' CHECK (employment IN ('full_time', 'part_time', 'contract', 'internship', 'temporary')),
  description   text NOT NULL DEFAULT '' CHECK (length(description) <= 8000),
  requirements  text NOT NULL DEFAULT '' CHECK (length(requirements) <= 4000),
  salary_range  text NOT NULL DEFAULT '' CHECK (length(salary_range) <= 80),
  vacancies     int NOT NULL DEFAULT 1 CHECK (vacancies BETWEEN 1 AND 1000),
  is_published  boolean NOT NULL DEFAULT false,
  status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  created_by    uuid DEFAULT app_user(),
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id)
);

CREATE TABLE job_applicants (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id        uuid NOT NULL,
  number            bigint NOT NULL,
  job_id            uuid,
  name              text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 160),
  email             text NOT NULL DEFAULT '' CHECK (length(email) <= 200),
  phone             text NOT NULL DEFAULT '' CHECK (length(phone) <= 40),
  source            text NOT NULL DEFAULT 'manual' CHECK (source IN ('careers', 'manual', 'referral', 'other')),
  stage             text NOT NULL DEFAULT 'new' CHECK (stage IN ('new', 'screening', 'interview', 'offer', 'hired', 'refused')),
  rating            smallint NOT NULL DEFAULT 0 CHECK (rating BETWEEN 0 AND 5),
  cover_letter      text NOT NULL DEFAULT '' CHECK (length(cover_letter) <= 8000),
  expected_salary   text NOT NULL DEFAULT '' CHECK (length(expected_salary) <= 60),
  notes             text NOT NULL DEFAULT '' CHECK (length(notes) <= 4000),
  offer_salary      numeric(20, 4) CHECK (offer_salary IS NULL OR offer_salary >= 0),
  offer_currency    char(3) REFERENCES currencies (code),
  offer_start       date,
  offer_sent_at     timestamptz,
  hired_employee_id uuid,
  refuse_reason     text NOT NULL DEFAULT '' CHECK (length(refuse_reason) <= 500),
  ip                text NOT NULL DEFAULT '',
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT job_applicants_job_id_same_company FOREIGN KEY (company_id, job_id) REFERENCES job_positions (company_id, id),
  FOREIGN KEY (tenant_id, hired_employee_id) REFERENCES employees (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
CREATE INDEX job_applicants_job_idx ON job_applicants (company_id, job_id, stage);

CREATE TABLE job_interviews (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  applicant_id   uuid NOT NULL,
  scheduled_at   timestamptz NOT NULL,
  duration_min   int NOT NULL DEFAULT 45 CHECK (duration_min BETWEEN 5 AND 600),
  interviewer_id uuid REFERENCES users (id),
  kind           text NOT NULL DEFAULT 'onsite' CHECK (kind IN ('phone', 'video', 'onsite')),
  location       text NOT NULL DEFAULT '' CHECK (length(location) <= 200),
  status         text NOT NULL DEFAULT 'planned' CHECK (status IN ('planned', 'done', 'cancelled')),
  rating         smallint CHECK (rating IS NULL OR rating BETWEEN 1 AND 5),
  feedback       text NOT NULL DEFAULT '' CHECK (length(feedback) <= 4000),
  created_at     timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT job_interviews_applicant_id_same_company FOREIGN KEY (company_id, applicant_id) REFERENCES job_applicants (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX job_interviews_applicant_idx ON job_interviews (applicant_id, scheduled_at);

-- ======================= appraisals =======================
CREATE TABLE appraisal_cycles (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  name        text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
  period_from date NOT NULL,
  period_to   date NOT NULL,
  due_date    date,
  self_review boolean NOT NULL DEFAULT true,
  status      text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'open', 'closed')),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id),
  CHECK (period_to >= period_from)
);

CREATE TABLE appraisals (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id      uuid NOT NULL,
  cycle_id        uuid NOT NULL,
  employee_id     uuid NOT NULL,
  reviewer_id     uuid REFERENCES users (id),
  goals           jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(goals) = 'array' AND jsonb_array_length(goals) <= 30),
  self_comment    text NOT NULL DEFAULT '' CHECK (length(self_comment) <= 4000),
  manager_comment text NOT NULL DEFAULT '' CHECK (length(manager_comment) <= 4000),
  strengths       text NOT NULL DEFAULT '' CHECK (length(strengths) <= 2000),
  improvements    text NOT NULL DEFAULT '' CHECK (length(improvements) <= 2000),
  self_score      numeric(4, 2) CHECK (self_score IS NULL OR self_score BETWEEN 0 AND 5),
  manager_score   numeric(4, 2) CHECK (manager_score IS NULL OR manager_score BETWEEN 0 AND 5),
  final_score     numeric(4, 2) CHECK (final_score IS NULL OR final_score BETWEEN 0 AND 5),
  status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'self_done', 'reviewed', 'closed')),
  self_at         timestamptz,
  reviewed_at     timestamptz,
  closed_at       timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT appraisals_cycle_id_same_company FOREIGN KEY (company_id, cycle_id) REFERENCES appraisal_cycles (company_id, id),
  FOREIGN KEY (tenant_id, employee_id) REFERENCES employees (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (cycle_id, employee_id)
);
CREATE INDEX appraisals_employee_idx ON appraisals (company_id, employee_id);

-- ======================= e-sign =======================
CREATE TABLE sign_requests (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  title            text NOT NULL CHECK (length(trim(title)) BETWEEN 1 AND 200),
  doc_type         text NOT NULL CHECK (doc_type IN ('quote', 'contract', 'subscription', 'upload')),
  sales_order_id   uuid,
  subscription_id  uuid,
  attachment_id    uuid REFERENCES attachments (id),
  body             text NOT NULL DEFAULT '' CHECK (length(body) <= 60000),  -- the text that is signed (contract / snapshot)
  doc_hash         text NOT NULL DEFAULT '' CHECK (doc_hash = '' OR doc_hash ~ '^[0-9a-f]{64}$'),
  message          text NOT NULL DEFAULT '' CHECK (length(message) <= 2000),
  ordered          boolean NOT NULL DEFAULT true,
  channel          text NOT NULL DEFAULT 'whatsapp' CHECK (channel IN ('whatsapp', 'sms', 'email', 'none')),
  status           text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'sent', 'completed', 'declined', 'cancelled', 'expired')),
  expires_on       date,
  auto_confirm     boolean NOT NULL DEFAULT false,
  remind_days      int NOT NULL DEFAULT 3 CHECK (remind_days BETWEEN 0 AND 30),
  last_reminded_at timestamptz,
  sent_at          timestamptz,
  completed_at     timestamptz,
  certificate_id   uuid REFERENCES attachments (id),
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT sign_requests_sales_order_id_same_company FOREIGN KEY (company_id, sales_order_id) REFERENCES sales_orders (company_id, id),
  CONSTRAINT sign_requests_subscription_id_same_company FOREIGN KEY (company_id, subscription_id) REFERENCES subscriptions (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
CREATE INDEX sign_requests_open_idx ON sign_requests (company_id, status);

CREATE TABLE sign_signers (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  request_id     uuid NOT NULL,
  seq            int NOT NULL,
  name           text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 160),
  email          text NOT NULL DEFAULT '' CHECK (length(email) <= 200),
  phone          text NOT NULL DEFAULT '' CHECK (length(phone) <= 40),
  customer_id    uuid,
  token          text NOT NULL CHECK (length(token) >= 32),
  status         text NOT NULL DEFAULT 'waiting' CHECK (status IN ('waiting', 'pending', 'viewed', 'signed', 'declined')),
  viewed_at      timestamptz,
  signed_at      timestamptz,
  declined_at    timestamptz,
  decline_reason text NOT NULL DEFAULT '' CHECK (length(decline_reason) <= 1000),
  sign_method    text NOT NULL DEFAULT '' CHECK (sign_method IN ('', 'draw', 'type')),
  typed_name     text NOT NULL DEFAULT '' CHECK (length(typed_name) <= 160),
  signature      text NOT NULL DEFAULT '' CHECK (signature = '' OR (signature LIKE 'data:image/png;base64,%' AND length(signature) <= 300000)),
  ip             text NOT NULL DEFAULT '',
  user_agent     text NOT NULL DEFAULT '' CHECK (length(user_agent) <= 400),
  notified_at    timestamptz,
  reminders      int NOT NULL DEFAULT 0,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT sign_signers_request_id_same_company FOREIGN KEY (company_id, request_id) REFERENCES sign_requests (company_id, id),
  CONSTRAINT sign_signers_customer_id_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (token),
  CHECK (seq BETWEEN 1 AND 10)
);
CREATE INDEX sign_signers_request_idx ON sign_signers (request_id, seq);

CREATE TABLE sign_events (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  request_id uuid NOT NULL,
  signer_id  uuid,
  event      text NOT NULL CHECK (event IN ('created', 'sent', 'viewed', 'signed', 'declined', 'reminded', 'completed', 'cancelled', 'expired', 'confirmed')),
  ip         text NOT NULL DEFAULT '',
  user_agent text NOT NULL DEFAULT '' CHECK (length(user_agent) <= 400),
  detail     text NOT NULL DEFAULT '' CHECK (length(detail) <= 1000),
  created_by uuid DEFAULT app_user(),
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT sign_events_request_id_same_company FOREIGN KEY (company_id, request_id) REFERENCES sign_requests (company_id, id),
  CONSTRAINT sign_events_signer_id_same_company FOREIGN KEY (company_id, signer_id) REFERENCES sign_signers (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX sign_events_request_idx ON sign_events (request_id, created_at);
CREATE TRIGGER sign_events_immutable BEFORE UPDATE OR DELETE ON sign_events FOR EACH ROW EXECUTE FUNCTION forbid_change();

-- ---------- public lookups (callable before any tenant is known; they return ids only) ----------
-- +goose StatementBegin
-- public_lookup: the company of a shop / careers address
CREATE FUNCTION public_lookup(p_slug text) RETURNS TABLE (tenant_id uuid, company_id uuid, shop_on boolean, careers_on boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT c.tenant_id, c.id, coalesce(s.enabled, false) AND 'web' = ANY (c.apps), coalesce(s.careers_enabled, false) AND 'hr' = ANY (c.apps)
  FROM companies c JOIN tenants t ON t.id = c.tenant_id LEFT JOIN shop_settings s ON s.company_id = c.id
  WHERE c.public_slug = lower(p_slug) AND c.is_active AND t.is_active AND tenant_jobs_ok(t.id)
$$;
-- shop_order_lookup: the order of a tracking link
CREATE FUNCTION shop_order_lookup(p_token text) RETURNS TABLE (tenant_id uuid, company_id uuid, order_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT o.tenant_id, o.company_id, o.id FROM shop_orders o JOIN tenants t ON t.id = o.tenant_id
  WHERE o.token = p_token AND t.is_active AND length(p_token) >= 32
$$;
-- sign_lookup: the signer of a signing link
CREATE FUNCTION sign_lookup(p_token text) RETURNS TABLE (tenant_id uuid, company_id uuid, signer_id uuid, request_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT s.tenant_id, s.company_id, s.id, s.request_id FROM sign_signers s JOIN tenants t ON t.id = s.tenant_id
  WHERE s.token = p_token AND t.is_active AND length(p_token) >= 32
$$;
-- mkt_lookup: the campaign recipient of a tracked link / opt-out link
CREATE FUNCTION mkt_lookup(p_token text) RETURNS TABLE (tenant_id uuid, company_id uuid, recipient_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT r.tenant_id, r.company_id, r.id FROM mkt_recipients r JOIN tenants t ON t.id = r.tenant_id
  WHERE r.token = p_token AND t.is_active AND length(p_token) >= 32
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION public_lookup(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION shop_order_lookup(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION sign_lookup(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION mkt_lookup(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public_lookup(text) TO paxve_app;
GRANT EXECUTE ON FUNCTION shop_order_lookup(text) TO paxve_app;
GRANT EXECUTE ON FUNCTION sign_lookup(text) TO paxve_app;
GRANT EXECUTE ON FUNCTION mkt_lookup(text) TO paxve_app;

-- ---------- row security, grants and the audit trail ----------
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['shop_settings', 'shop_zones', 'shop_orders', 'mkt_lists', 'mkt_list_members', 'mkt_optouts', 'mkt_campaigns',
                           'mkt_recipients', 'job_positions', 'job_applicants', 'job_interviews', 'appraisal_cycles', 'appraisals',
                           'sign_requests', 'sign_signers', 'sign_events'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
    IF t NOT IN ('mkt_recipients', 'mkt_list_members', 'sign_events') THEN
      EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    END IF;
  END LOOP;
END
$$;
-- +goose StatementEnd
GRANT DELETE ON shop_zones, mkt_list_members, mkt_optouts, job_interviews TO paxve_app;

-- +goose Down
DROP TABLE IF EXISTS sign_events, sign_signers, sign_requests, appraisals, appraisal_cycles, job_interviews, job_applicants, job_positions,
  mkt_recipients, mkt_campaigns, mkt_optouts, mkt_list_members, mkt_lists, shop_orders, shop_zones, shop_settings CASCADE;
DROP FUNCTION IF EXISTS public_lookup(text);
DROP FUNCTION IF EXISTS shop_order_lookup(text);
DROP FUNCTION IF EXISTS sign_lookup(text);
DROP FUNCTION IF EXISTS mkt_lookup(text);
ALTER TABLE items DROP COLUMN IF EXISTS shop_visible, DROP COLUMN IF EXISTS shop_description;
ALTER TABLE sales_orders DROP COLUMN IF EXISTS source;
ALTER TABLE companies DROP COLUMN IF EXISTS public_slug;
