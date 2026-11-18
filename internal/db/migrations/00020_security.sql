-- Module P: permissions, security and integrations.
--   * roles: custom roles per tenant (permission lists); users.role stays the built-in "base" role
--   * users.company_ids: optional list of companies a user may open (NULL = all)
--   * two-factor authentication (TOTP), tracked sessions, password reset links, password policy
--   * approval requests for voids / credit notes / large payments (company approval_rules)
--   * API key scopes, outbound webhooks (signed, retried) fed by deferred triggers

-- +goose Up

-- ---- custom roles ----
CREATE TABLE roles (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  name        text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 80),
  base        text NOT NULL CHECK (base IN ('admin', 'accountant', 'data_entry', 'read_only', 'collector', 'cashier')),
  permissions text[] NOT NULL DEFAULT '{}',
  description text NOT NULL DEFAULT '',
  is_active   boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, name)
);
CREATE TRIGGER roles_touch BEFORE UPDATE ON roles FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER roles_audit AFTER INSERT OR UPDATE OR DELETE ON roles FOR EACH ROW EXECUTE FUNCTION audit_row();

ALTER TABLE users ADD COLUMN role_id uuid;
ALTER TABLE users ADD CONSTRAINT users_role_fk FOREIGN KEY (tenant_id, role_id) REFERENCES roles (tenant_id, id);
ALTER TABLE users ADD COLUMN company_ids uuid[];     -- NULL = every company of the organisation
ALTER TABLE users ADD COLUMN password_changed_at timestamptz;

-- ---- organisation security settings ----
ALTER TABLE tenants ADD COLUMN require_mfa boolean NOT NULL DEFAULT false;
ALTER TABLE tenants ADD COLUMN password_min_length int NOT NULL DEFAULT 10 CHECK (password_min_length BETWEEN 10 AND 64);
GRANT UPDATE (require_mfa, password_min_length) ON tenants TO paxve_app;

-- ---- two-factor authentication (secrets kept out of the users table and out of the audit log) ----
CREATE TABLE user_mfa (
  user_id          uuid PRIMARY KEY,
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  secret_enc       text NOT NULL DEFAULT '',
  pending_enc      text NOT NULL DEFAULT '',          -- secret being set up, not yet confirmed
  enabled_at       timestamptz,
  recovery_hashes  text[] NOT NULL DEFAULT '{}',      -- sha256 hex of unused recovery codes
  last_step        bigint NOT NULL DEFAULT 0,         -- last accepted TOTP time step (no replay)
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id)
);
CREATE TRIGGER user_mfa_touch BEFORE UPDATE ON user_mfa FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ---- tracked sign-in sessions (the cookie session stores the id; revoking ends it at the next request) ----
CREATE TABLE user_sessions (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  user_id      uuid NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  ip           inet,
  user_agent   text NOT NULL DEFAULT '',
  mfa          boolean NOT NULL DEFAULT false,
  revoked_at   timestamptz,
  revoked_by   uuid,
  FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id)
);
CREATE INDEX user_sessions_user_idx ON user_sessions (tenant_id, user_id, last_seen_at DESC);

-- ---- password reset links (only the SHA-256 of the token is stored) ----
CREATE TABLE password_resets (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  user_id     uuid NOT NULL,
  token_hash  bytea NOT NULL UNIQUE,
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  expires_at  timestamptz NOT NULL,
  used_at     timestamptz,
  FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id)
);
CREATE TRIGGER password_resets_audit AFTER INSERT ON password_resets FOR EACH ROW EXECUTE FUNCTION audit_row();

-- A reset link is opened before anyone is signed in: one narrow cross-tenant lookup by token hash.
-- +goose StatementBegin
CREATE FUNCTION password_reset_lookup(p_hash bytea)
RETURNS TABLE (id uuid, tenant_id uuid, user_id uuid, email text, valid boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT r.id, r.tenant_id, r.user_id, u.email,
         (r.used_at IS NULL AND r.expires_at > now() AND u.is_active AND t.is_active)
  FROM password_resets r
  JOIN users u ON u.id = r.user_id
  JOIN tenants t ON t.id = r.tenant_id
  WHERE r.token_hash = p_hash
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION password_reset_lookup(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION password_reset_lookup(bytea) TO paxve_app;

-- ---- approvals ----
-- rules: {"void": bool, "credit_note": bool, "payment_over": "<amount in base currency>" | null}
ALTER TABLE companies ADD COLUMN approval_rules jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE TABLE approval_requests (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  kind          text NOT NULL CHECK (kind IN ('void', 'credit_note', 'payment')),
  permission    text NOT NULL,                       -- permission of the action (approver needs <module>.approve)
  method        text NOT NULL,
  pattern       text NOT NULL,                       -- mux pattern of the action
  path          text NOT NULL,                       -- request path to replay
  body          jsonb,
  summary       text NOT NULL DEFAULT '',
  amount        numeric(20, 6),
  currency      char(3),
  status        text NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'failed')),
  requested_by  uuid NOT NULL DEFAULT app_user(),
  requested_at  timestamptz NOT NULL DEFAULT now(),
  decided_by    uuid,
  decided_at    timestamptz,
  note          text NOT NULL DEFAULT '',
  result        jsonb,
  error         text,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id)
);
CREATE INDEX approval_requests_company_idx ON approval_requests (tenant_id, company_id, status, requested_at DESC);
CREATE TRIGGER approval_requests_audit AFTER INSERT OR UPDATE ON approval_requests FOR EACH ROW EXECUTE FUNCTION audit_row();

-- ---- API key scopes (NULL = everything the key's role allows) ----
ALTER TABLE api_keys ADD COLUMN scopes text[];
ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_role_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_role_check CHECK (role IN ('admin', 'accountant', 'data_entry', 'read_only')) NOT VALID;

-- ---- webhooks ----
CREATE TABLE webhooks (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  url          text NOT NULL CHECK (url ~ '^https?://' AND length(url) <= 2000),
  description  text NOT NULL DEFAULT '',
  secret_enc   text NOT NULL,
  events       text[] NOT NULL CHECK (cardinality(events) > 0),
  is_active    boolean NOT NULL DEFAULT true,
  created_by   uuid DEFAULT app_user(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id)
);
CREATE TRIGGER webhooks_touch BEFORE UPDATE ON webhooks FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- audit: never copy secrets (webhook secret, MFA) into the audit log
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_row() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_old    jsonb;
  v_new    jsonb;
  v_tenant uuid;
  v_id     uuid;
BEGIN
  IF TG_OP <> 'INSERT' THEN v_old := to_jsonb(OLD) - 'password_hash' - 'key_hash' - 'updated_at' - 'last_used_at' - 'config' - 'password_enc' - 'secret_enc' - 'token_hash'; END IF;
  IF TG_OP <> 'DELETE' THEN v_new := to_jsonb(NEW) - 'password_hash' - 'key_hash' - 'updated_at' - 'last_used_at' - 'config' - 'password_enc' - 'secret_enc' - 'token_hash'; END IF;

  IF TG_OP = 'UPDATE' THEN
    IF (to_jsonb(OLD) ->> 'password_hash') IS DISTINCT FROM (to_jsonb(NEW) ->> 'password_hash')
       OR (to_jsonb(OLD) ->> 'password_enc') IS DISTINCT FROM (to_jsonb(NEW) ->> 'password_enc') THEN
      v_new := v_new || '{"password_changed": true}'::jsonb;
    END IF;
    IF (to_jsonb(OLD) -> 'config') IS DISTINCT FROM (to_jsonb(NEW) -> 'config') THEN
      v_new := v_new || '{"config_changed": true}'::jsonb;
    END IF;
    IF (to_jsonb(OLD) ->> 'secret_enc') IS DISTINCT FROM (to_jsonb(NEW) ->> 'secret_enc') THEN
      v_new := v_new || '{"secret_changed": true}'::jsonb;
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
CREATE TRIGGER webhooks_audit AFTER INSERT OR UPDATE ON webhooks FOR EACH ROW EXECUTE FUNCTION audit_row();

CREATE TABLE webhook_deliveries (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id      uuid NOT NULL,
  webhook_id      uuid NOT NULL,
  event           text NOT NULL,
  payload         jsonb NOT NULL,
  status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
  attempts        int NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  response_code   int,
  last_error      text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  sent_at         timestamptz,
  FOREIGN KEY (tenant_id, webhook_id) REFERENCES webhooks (tenant_id, id)
);
CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries (next_attempt_at) WHERE status = 'pending';
CREATE INDEX webhook_deliveries_hook_idx ON webhook_deliveries (tenant_id, webhook_id, created_at DESC);

-- Deferred (runs at commit), so the payload is the row as committed, e.g. an invoice with its totals.
-- +goose StatementBegin
CREATE FUNCTION webhook_emit() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_event text := TG_ARGV[0];
  v_data  jsonb;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM webhooks w WHERE w.tenant_id = NEW.tenant_id AND w.company_id = NEW.company_id
                 AND w.is_active AND v_event = ANY (w.events)) THEN
    RETURN NULL;
  END IF;
  EXECUTE format('SELECT to_jsonb(x) FROM %I x WHERE x.id = $1', TG_TABLE_NAME) INTO v_data USING NEW.id;
  IF v_data IS NULL THEN
    RETURN NULL;
  END IF;
  IF TG_TABLE_NAME = 'payments' AND v_data ->> 'kind' <> 'payment' THEN
    RETURN NULL;
  END IF;
  v_data := v_data - 'tenant_id' - 'portal_token' - 'client_ref' - 'search' - 'updated_at';
  INSERT INTO webhook_deliveries (tenant_id, company_id, webhook_id, event, payload)
  SELECT NEW.tenant_id, NEW.company_id, w.id, v_event,
         jsonb_build_object('id', gen_random_uuid(), 'event', v_event, 'created_at', now(),
                            'company_id', NEW.company_id, 'data', v_data)
  FROM webhooks w
  WHERE w.tenant_id = NEW.tenant_id AND w.company_id = NEW.company_id AND w.is_active AND v_event = ANY (w.events);
  RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER invoices_webhook AFTER INSERT ON invoices DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION webhook_emit('invoice.created');
CREATE CONSTRAINT TRIGGER payments_webhook AFTER INSERT ON payments DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION webhook_emit('payment.created');
CREATE CONSTRAINT TRIGGER customers_webhook AFTER INSERT ON customers DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION webhook_emit('customer.created');
CREATE CONSTRAINT TRIGGER pos_orders_webhook AFTER INSERT ON pos_orders DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION webhook_emit('pos.order.created');

-- ---- row-level security and grants ----
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['roles', 'user_mfa', 'user_sessions', 'password_resets', 'approval_requests', 'webhooks', 'webhook_deliveries'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
  END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS invoices_webhook ON invoices;
DROP TRIGGER IF EXISTS payments_webhook ON payments;
DROP TRIGGER IF EXISTS customers_webhook ON customers;
DROP TRIGGER IF EXISTS pos_orders_webhook ON pos_orders;
DROP FUNCTION IF EXISTS webhook_emit();
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhooks;
ALTER TABLE api_keys DROP COLUMN IF EXISTS scopes;
DROP TABLE IF EXISTS approval_requests;
ALTER TABLE companies DROP COLUMN IF EXISTS approval_rules;
DROP FUNCTION IF EXISTS password_reset_lookup(bytea);
DROP TABLE IF EXISTS password_resets;
DROP TABLE IF EXISTS user_sessions;
DROP TABLE IF EXISTS user_mfa;
ALTER TABLE tenants DROP COLUMN IF EXISTS password_min_length;
ALTER TABLE tenants DROP COLUMN IF EXISTS require_mfa;
ALTER TABLE users DROP COLUMN IF EXISTS password_changed_at;
ALTER TABLE users DROP COLUMN IF EXISTS company_ids;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_fk;
ALTER TABLE users DROP COLUMN IF EXISTS role_id;
DROP TABLE IF EXISTS roles;
