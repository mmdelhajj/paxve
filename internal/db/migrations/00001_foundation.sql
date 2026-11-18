-- Paxve foundation: tenants, companies, users, audit log, sessions.
-- Every business table carries tenant_id and is protected by row-level security.
-- The app connects as paxve_app (no superuser, NOBYPASSRLS) and sets
-- app.tenant_id / app.user_id / app.ip per transaction with set_config(..., true).

-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'paxve_app') THEN
    CREATE ROLE paxve_app LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOINHERIT;
  END IF;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA public TO paxve_app;

-- +goose StatementBegin
CREATE FUNCTION app_tenant() RETURNS uuid
LANGUAGE sql STABLE PARALLEL SAFE AS $$
  SELECT nullif(current_setting('app.tenant_id', true), '')::uuid
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app_user() RETURNS uuid
LANGUAGE sql STABLE PARALLEL SAFE AS $$
  SELECT nullif(current_setting('app.user_id', true), '')::uuid
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION touch_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TABLE tenants (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  name        text NOT NULL CHECK (length(trim(name)) > 0),
  slug        text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,40}$'),
  is_active   boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE companies (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  name_en        text NOT NULL CHECK (length(trim(name_en)) > 0),
  name_ar        text NOT NULL DEFAULT '',
  base_currency  char(3) NOT NULL DEFAULT 'USD',
  local_currency char(3) NOT NULL DEFAULT 'LBP',
  is_active      boolean NOT NULL DEFAULT true,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, name_en)
);

CREATE TABLE users (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  email         text NOT NULL CHECK (email = lower(email) AND position('@' IN email) > 1),
  full_name     text NOT NULL CHECK (length(trim(full_name)) > 0),
  password_hash text NOT NULL,
  role          text NOT NULL CHECK (role IN ('admin', 'accountant', 'data_entry', 'read_only')),
  locale        text NOT NULL DEFAULT 'en' CHECK (locale IN ('en', 'ar')),
  is_active     boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id)
);
-- Login is by email, so emails are unique across the whole platform.
CREATE UNIQUE INDEX users_email_key ON users (email);
CREATE INDEX users_tenant_idx ON users (tenant_id, email);

CREATE TRIGGER tenants_touch   BEFORE UPDATE ON tenants   FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER companies_touch BEFORE UPDATE ON companies FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER users_touch     BEFORE UPDATE ON users     FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- Audit log: append-only record of every change, with user, time and IP.
CREATE TABLE audit_log (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id  uuid NOT NULL REFERENCES tenants (id),
  user_id    uuid,
  at         timestamptz NOT NULL DEFAULT now(),
  action     text NOT NULL,
  entity     text NOT NULL,
  entity_id  uuid,
  before     jsonb,
  after      jsonb,
  ip         inet
);
CREATE INDEX audit_log_tenant_idx ON audit_log (tenant_id, id DESC);
CREATE INDEX audit_log_entity_idx ON audit_log (tenant_id, entity, entity_id);

-- +goose StatementBegin
CREATE FUNCTION audit_row() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_old    jsonb;
  v_new    jsonb;
  v_tenant uuid;
  v_id     uuid;
BEGIN
  IF TG_OP <> 'INSERT' THEN v_old := to_jsonb(OLD) - 'password_hash' - 'updated_at'; END IF;
  IF TG_OP <> 'DELETE' THEN v_new := to_jsonb(NEW) - 'password_hash' - 'updated_at'; END IF;

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

  INSERT INTO audit_log (tenant_id, user_id, action, entity, entity_id, before, after, ip)
  VALUES (v_tenant, app_user(), lower(TG_OP), TG_TABLE_NAME, v_id, v_old, v_new,
          nullif(current_setting('app.ip', true), '')::inet);
  RETURN coalesce(NEW, OLD);
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION forbid_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER tenants_audit   AFTER INSERT OR UPDATE OR DELETE ON tenants   FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER companies_audit AFTER INSERT OR UPDATE OR DELETE ON companies FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER users_audit     AFTER INSERT OR UPDATE OR DELETE ON users     FOR EACH ROW EXECUTE FUNCTION audit_row();

CREATE TRIGGER audit_log_no_update BEFORE UPDATE OR DELETE ON audit_log FOR EACH ROW EXECUTE FUNCTION forbid_change();
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log FOR EACH STATEMENT EXECUTE FUNCTION forbid_change();

-- Web sessions (alexedwards/scs pgxstore). Keyed by a random token, not tenant data.
CREATE TABLE sessions (
  token  text PRIMARY KEY,
  data   bytea NOT NULL,
  expiry timestamptz NOT NULL
);
CREATE INDEX sessions_expiry_idx ON sessions (expiry);

-- Login runs before the tenant is known, so it needs one narrow lookup that
-- crosses tenants. It returns only what is needed to check the password.
-- +goose StatementBegin
CREATE FUNCTION auth_lookup(p_email text)
RETURNS TABLE (user_id uuid, tenant_id uuid, password_hash text, is_active boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT u.id, u.tenant_id, u.password_hash, (u.is_active AND t.is_active)
  FROM users u
  JOIN tenants t ON t.id = u.tenant_id
  WHERE u.email = lower(trim(p_email))
$$;
-- +goose StatementEnd

-- Row-level security: a tenant only ever sees its own rows.
ALTER TABLE tenants   ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants   FORCE ROW LEVEL SECURITY;
ALTER TABLE companies ENABLE ROW LEVEL SECURITY;
ALTER TABLE companies FORCE ROW LEVEL SECURITY;
ALTER TABLE users     ENABLE ROW LEVEL SECURITY;
ALTER TABLE users     FORCE ROW LEVEL SECURITY;
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_self ON tenants
  USING (id = app_tenant());
CREATE POLICY tenant_isolation ON companies
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON users
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON audit_log
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());

-- The app role may read and write, but never delete business rows.
GRANT SELECT ON tenants TO paxve_app;
GRANT SELECT, INSERT, UPDATE ON companies, users TO paxve_app;
GRANT SELECT, INSERT ON audit_log TO paxve_app;
GRANT USAGE ON SEQUENCE audit_log_id_seq TO paxve_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON sessions TO paxve_app;
REVOKE ALL ON FUNCTION auth_lookup(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_lookup(text) TO paxve_app;

-- +goose Down
DROP FUNCTION IF EXISTS auth_lookup(text);
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS companies;
DROP TABLE IF EXISTS tenants;
DROP FUNCTION IF EXISTS forbid_change();
DROP FUNCTION IF EXISTS audit_row();
DROP FUNCTION IF EXISTS touch_updated_at();
DROP FUNCTION IF EXISTS app_user();
DROP FUNCTION IF EXISTS app_tenant();
