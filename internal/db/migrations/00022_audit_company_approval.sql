-- Module Q: the audit log records the company of each change and, for an approved request, who
-- approved it (the change itself is made in the requester's name).

-- +goose Up
ALTER TABLE approval_requests DROP CONSTRAINT approval_requests_kind_check;
ALTER TABLE approval_requests ADD CONSTRAINT approval_requests_kind_check CHECK (kind IN ('void', 'credit_note', 'payment', 'refund'));

ALTER TABLE audit_log ADD COLUMN company_id uuid;
ALTER TABLE audit_log ADD COLUMN approved_by uuid;
ALTER TABLE audit_log ADD COLUMN approval_id uuid;
CREATE INDEX audit_log_company_idx ON audit_log (tenant_id, company_id, id DESC);
CREATE INDEX audit_log_entity_id_idx ON audit_log (tenant_id, entity_id, id DESC);

SET session_replication_role = replica;
UPDATE audit_log SET company_id = coalesce(after ->> 'company_id', before ->> 'company_id',
  CASE WHEN entity = 'companies' THEN entity_id::text END)::uuid
WHERE coalesce(after ->> 'company_id', before ->> 'company_id') IS NOT NULL OR entity = 'companies';
SET session_replication_role = origin;

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

  INSERT INTO audit_log (tenant_id, user_id, api_key_id, action, entity, entity_id, before, after, ip,
                         company_id, approved_by, approval_id)
  VALUES (v_tenant, app_user(), nullif(current_setting('app.api_key_id', true), '')::uuid,
          lower(TG_OP), TG_TABLE_NAME, v_id, v_old, v_new,
          nullif(current_setting('app.ip', true), '')::inet,
          coalesce(v_new ->> 'company_id', v_old ->> 'company_id',
                   CASE WHEN TG_TABLE_NAME = 'companies' THEN v_id::text END)::uuid,
          nullif(current_setting('app.approved_by', true), '')::uuid,
          nullif(current_setting('app.approval_id', true), '')::uuid);
  RETURN coalesce(NEW, OLD);
END
$$;
-- +goose StatementEnd

-- +goose Down
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
  IF TG_OP = 'UPDATE' AND v_old = v_new THEN
    RETURN NEW;
  END IF;
  v_tenant := coalesce(v_new ->> 'tenant_id', v_old ->> 'tenant_id', v_new ->> 'id', v_old ->> 'id')::uuid;
  v_id     := coalesce(v_new ->> 'id', v_old ->> 'id')::uuid;
  INSERT INTO audit_log (tenant_id, user_id, api_key_id, action, entity, entity_id, before, after, ip)
  VALUES (v_tenant, app_user(), nullif(current_setting('app.api_key_id', true), '')::uuid,
          lower(TG_OP), TG_TABLE_NAME, v_id, v_old, v_new, nullif(current_setting('app.ip', true), '')::inet);
  RETURN coalesce(NEW, OLD);
END
$$;
-- +goose StatementEnd
DROP INDEX IF EXISTS audit_log_entity_id_idx;
DROP INDEX IF EXISTS audit_log_company_idx;
ALTER TABLE audit_log DROP COLUMN approval_id;
ALTER TABLE audit_log DROP COLUMN approved_by;
ALTER TABLE audit_log DROP COLUMN company_id;
ALTER TABLE approval_requests DROP CONSTRAINT approval_requests_kind_check;
ALTER TABLE approval_requests ADD CONSTRAINT approval_requests_kind_check CHECK (kind IN ('void', 'credit_note', 'payment'));
