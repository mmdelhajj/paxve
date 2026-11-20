-- Phase 6b: demo requests from the marketing site (platform leads), catalog prices lowered by hand at the till
-- (smart alert "price_override"), and the time an offline device really took a payment.
-- +goose Up

CREATE TABLE platform_leads (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  name       text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
  company    text NOT NULL DEFAULT '' CHECK (length(company) <= 160),
  phone      text NOT NULL DEFAULT '' CHECK (length(phone) <= 40),
  email      text NOT NULL DEFAULT '' CHECK (length(email) <= 200),
  business   text NOT NULL DEFAULT 'other' CHECK (length(business) <= 30),
  city       text NOT NULL DEFAULT '' CHECK (length(city) <= 80),
  message    text NOT NULL DEFAULT '' CHECK (length(message) <= 2000),
  lang       text NOT NULL DEFAULT '' CHECK (length(lang) <= 5),
  ip         text NOT NULL DEFAULT '',
  status     text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'contacted', 'won', 'lost')),
  notes      text NOT NULL DEFAULT '' CHECK (length(notes) <= 2000),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX platform_leads_status_idx ON platform_leads (status, created_at DESC);
-- only the platform owner (owner pool) reads and writes leads: no grant to the application role

ALTER TABLE pos_orders
  ADD COLUMN price_cut       numeric(20, 4) NOT NULL DEFAULT 0 CHECK (price_cut >= 0),
  ADD COLUMN price_cut_lines int NOT NULL DEFAULT 0 CHECK (price_cut_lines >= 0);

ALTER TABLE payments ADD COLUMN client_time timestamptz;

ALTER TABLE smart_alerts DROP CONSTRAINT IF EXISTS smart_alerts_kind_check;
ALTER TABLE smart_alerts ADD CONSTRAINT smart_alerts_kind_check CHECK (kind IN ('diesel', 'discount', 'cash_diff', 'cash_repeat', 'voids',
  'off_hours', 'count_loss', 'collector_drop', 'dup_bill', 'fleet_fuel', 'price_override'));

-- +goose Down
ALTER TABLE smart_alerts DROP CONSTRAINT IF EXISTS smart_alerts_kind_check;
DELETE FROM smart_alerts WHERE kind = 'price_override';
ALTER TABLE smart_alerts ADD CONSTRAINT smart_alerts_kind_check CHECK (kind IN ('diesel', 'discount', 'cash_diff', 'cash_repeat', 'voids',
  'off_hours', 'count_loss', 'collector_drop', 'dup_bill', 'fleet_fuel'));
ALTER TABLE payments DROP COLUMN IF EXISTS client_time;
ALTER TABLE pos_orders DROP COLUMN IF EXISTS price_cut_lines, DROP COLUMN IF EXISTS price_cut;
DROP TABLE IF EXISTS platform_leads;
