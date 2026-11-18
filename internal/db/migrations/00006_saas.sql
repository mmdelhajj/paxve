-- SaaS: plans, trials and limits per organisation.

-- +goose Up
ALTER TABLE tenants
  ADD COLUMN plan text NOT NULL DEFAULT 'trial' CHECK (plan IN ('trial', 'starter', 'business', 'pro')),
  ADD COLUMN trial_ends_at timestamptz,
  ADD COLUMN max_subscribers int CHECK (max_subscribers IS NULL OR max_subscribers >= 0),
  ADD COLUMN suspended_reason text NOT NULL DEFAULT '';

-- Organisations created before self sign-up existed keep full access.
UPDATE tenants SET plan = 'pro';

-- +goose Down
ALTER TABLE tenants DROP COLUMN IF EXISTS suspended_reason, DROP COLUMN IF EXISTS max_subscribers,
  DROP COLUMN IF EXISTS trial_ends_at, DROP COLUMN IF EXISTS plan;
