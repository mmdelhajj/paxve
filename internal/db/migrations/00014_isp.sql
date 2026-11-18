-- ISP automation: MikroTik routers, PPPoE/hotspot users on subscriptions, automatic
-- suspend when bills are overdue and reconnect after payment (through a queue of router actions).
-- Router passwords are stored encrypted by the application (AES-256-GCM, APP_SECRET_KEY).

-- +goose Up

CREATE TABLE routers (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  name          text NOT NULL CHECK (length(trim(name)) > 0),
  host          text NOT NULL CHECK (length(trim(host)) > 0),
  port          int NOT NULL DEFAULT 8728 CHECK (port BETWEEN 1 AND 65535),
  username      text NOT NULL,
  password_enc  text NOT NULL DEFAULT '',
  use_tls       boolean NOT NULL DEFAULT false,
  enabled       boolean NOT NULL DEFAULT true,
  last_check_at timestamptz,
  last_status   text NOT NULL DEFAULT '',
  identity      text NOT NULL DEFAULT '',
  version       text NOT NULL DEFAULT '',
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, name)
);
CREATE TRIGGER routers_touch BEFORE UPDATE ON routers FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

ALTER TABLE subscriptions
  ADD COLUMN router_id uuid,
  ADD COLUMN network_username text NOT NULL DEFAULT '',
  ADD COLUMN service_type text NOT NULL DEFAULT 'pppoe' CHECK (service_type IN ('pppoe', 'hotspot')),
  ADD COLUMN network_status text NOT NULL DEFAULT '' CHECK (network_status IN ('', 'enabled', 'disabled')),
  ADD COLUMN suspended_reason text NOT NULL DEFAULT '',
  ADD FOREIGN KEY (tenant_id, router_id) REFERENCES routers (tenant_id, id);
CREATE UNIQUE INDEX subscriptions_router_user_key ON subscriptions (tenant_id, router_id, network_username)
  WHERE router_id IS NOT NULL AND network_username <> '';

ALTER TABLE companies
  ADD COLUMN auto_suspend_enabled boolean NOT NULL DEFAULT false,
  ADD COLUMN suspend_after_days int NOT NULL DEFAULT 10 CHECK (suspend_after_days BETWEEN 0 AND 365),
  ADD COLUMN reconnect_on_payment boolean NOT NULL DEFAULT true;

-- Work for the router worker. Calls to routers never happen inside a database transaction:
-- the rule or the payment only queues an action; the worker sends it and retries on failure.
CREATE TABLE router_actions (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id      uuid NOT NULL,
  router_id       uuid NOT NULL,
  subscription_id uuid NOT NULL,
  action          text NOT NULL CHECK (action IN ('disable', 'enable')),
  reason          text NOT NULL DEFAULT 'manual' CHECK (reason IN ('overdue', 'payment', 'manual')),
  status          text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sending', 'sent', 'failed', 'cancelled')),
  attempts        int NOT NULL DEFAULT 0,
  error           text NOT NULL DEFAULT '',
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  created_by      uuid DEFAULT app_user(),
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  sent_at         timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, router_id) REFERENCES routers (tenant_id, id),
  FOREIGN KEY (tenant_id, subscription_id) REFERENCES subscriptions (tenant_id, id)
);
CREATE INDEX router_actions_queue_idx ON router_actions (status, next_attempt_at);
CREATE INDEX router_actions_sub_idx ON router_actions (tenant_id, subscription_id, created_at DESC);
CREATE TRIGGER router_actions_touch BEFORE UPDATE ON router_actions FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- Audit: router passwords (even encrypted) never go into the audit log.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_row() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_old    jsonb;
  v_new    jsonb;
  v_tenant uuid;
  v_id     uuid;
BEGIN
  IF TG_OP <> 'INSERT' THEN v_old := to_jsonb(OLD) - 'password_hash' - 'key_hash' - 'updated_at' - 'last_used_at' - 'config' - 'password_enc'; END IF;
  IF TG_OP <> 'DELETE' THEN v_new := to_jsonb(NEW) - 'password_hash' - 'key_hash' - 'updated_at' - 'last_used_at' - 'config' - 'password_enc'; END IF;

  IF TG_OP = 'UPDATE' THEN
    IF (to_jsonb(OLD) ->> 'password_hash') IS DISTINCT FROM (to_jsonb(NEW) ->> 'password_hash')
       OR (to_jsonb(OLD) ->> 'password_enc') IS DISTINCT FROM (to_jsonb(NEW) ->> 'password_enc') THEN
      v_new := v_new || '{"password_changed": true}'::jsonb;
    END IF;
    IF (to_jsonb(OLD) -> 'config') IS DISTINCT FROM (to_jsonb(NEW) -> 'config') THEN
      v_new := v_new || '{"config_changed": true}'::jsonb;
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

CREATE TRIGGER routers_audit AFTER INSERT OR UPDATE OR DELETE ON routers FOR EACH ROW EXECUTE FUNCTION audit_row();
-- actions: creation and final outcome are audited by the app writes (status changes are frequent; audit inserts only)
CREATE TRIGGER router_actions_audit AFTER INSERT ON router_actions FOR EACH ROW EXECUTE FUNCTION audit_row();

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['routers', 'router_actions'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
  END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS router_actions;
DROP INDEX IF EXISTS subscriptions_router_user_key;
ALTER TABLE subscriptions DROP COLUMN IF EXISTS router_id, DROP COLUMN IF EXISTS network_username,
  DROP COLUMN IF EXISTS service_type, DROP COLUMN IF EXISTS network_status, DROP COLUMN IF EXISTS suspended_reason;
DROP TABLE IF EXISTS routers;
ALTER TABLE companies DROP COLUMN IF EXISTS auto_suspend_enabled, DROP COLUMN IF EXISTS suspend_after_days,
  DROP COLUMN IF EXISTS reconnect_on_payment;
