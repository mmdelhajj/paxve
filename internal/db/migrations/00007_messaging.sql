-- Messaging: WhatsApp / SMS providers, message templates, and an outbox sent by a background worker.

-- +goose Up

ALTER TABLE companies
  ADD COLUMN auto_send_on_payment boolean NOT NULL DEFAULT false,
  ADD COLUMN auto_send_on_invoice boolean NOT NULL DEFAULT false,
  ADD COLUMN phone_country text NOT NULL DEFAULT '961' CHECK (phone_country ~ '^[1-9][0-9]{0,3}$');

CREATE TABLE message_providers (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  kind       text NOT NULL CHECK (kind IN ('whatsapp_cloud', 'twilio_sms', 'generic_http', 'log')),
  name       text NOT NULL CHECK (length(trim(name)) > 0),
  config     jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config) = 'object'),
  is_default boolean NOT NULL DEFAULT false,
  is_active  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, id)
);
CREATE UNIQUE INDEX message_providers_one_default ON message_providers (tenant_id, company_id) WHERE is_default;

CREATE TABLE message_templates (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  event       text NOT NULL CHECK (event IN ('invoice_issued', 'payment_received', 'reminder', 'custom')),
  locale      text NOT NULL DEFAULT 'ar' CHECK (locale ~ '^[a-z]{2}$'),
  name        text NOT NULL CHECK (length(trim(name)) > 0),
  body        text NOT NULL CHECK (length(trim(body)) > 0),
  wa_template text NOT NULL DEFAULT '',
  is_active   boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, event, locale, name)
);

CREATE TABLE messages (
  id                  uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id           uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id          uuid NOT NULL,
  customer_id         uuid,
  phone               text NOT NULL,
  channel             text NOT NULL CHECK (channel IN ('whatsapp', 'sms')),
  provider_id         uuid,
  template_id         uuid,
  event               text NOT NULL,
  body                text NOT NULL,
  params              jsonb NOT NULL DEFAULT '[]'::jsonb,
  status             text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sending', 'sent', 'failed')),
  error               text NOT NULL DEFAULT '',
  attempts            int NOT NULL DEFAULT 0,
  next_attempt_at     timestamptz NOT NULL DEFAULT now(),
  invoice_id          uuid,
  payment_id          uuid,
  provider_message_id text NOT NULL DEFAULT '',
  created_by          uuid DEFAULT app_user(),
  created_at          timestamptz NOT NULL DEFAULT now(),
  sent_at             timestamptz,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, customer_id) REFERENCES customers (tenant_id, id),
  FOREIGN KEY (tenant_id, provider_id) REFERENCES message_providers (tenant_id, id),
  FOREIGN KEY (tenant_id, template_id) REFERENCES message_templates (tenant_id, id),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoices (tenant_id, id),
  FOREIGN KEY (tenant_id, payment_id) REFERENCES payments (tenant_id, id)
);
CREATE INDEX messages_queue_idx ON messages (next_attempt_at) WHERE status = 'queued';
CREATE INDEX messages_list_idx ON messages (tenant_id, company_id, created_at DESC);
CREATE INDEX messages_customer_idx ON messages (tenant_id, customer_id, created_at DESC);

CREATE TRIGGER message_providers_touch BEFORE UPDATE ON message_providers FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER message_templates_touch BEFORE UPDATE ON message_templates FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER messages_touch BEFORE UPDATE ON messages FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- Audit: never copy provider secrets (config) into the audit log.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_row() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_old    jsonb;
  v_new    jsonb;
  v_tenant uuid;
  v_id     uuid;
BEGIN
  IF TG_OP <> 'INSERT' THEN v_old := to_jsonb(OLD) - 'password_hash' - 'key_hash' - 'updated_at' - 'last_used_at' - 'config'; END IF;
  IF TG_OP <> 'DELETE' THEN v_new := to_jsonb(NEW) - 'password_hash' - 'key_hash' - 'updated_at' - 'last_used_at' - 'config'; END IF;

  IF TG_OP = 'UPDATE' THEN
    IF (to_jsonb(OLD) ->> 'password_hash') IS DISTINCT FROM (to_jsonb(NEW) ->> 'password_hash') THEN
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

CREATE TRIGGER message_providers_audit AFTER INSERT OR UPDATE OR DELETE ON message_providers FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER message_templates_audit AFTER INSERT OR UPDATE OR DELETE ON message_templates FOR EACH ROW EXECUTE FUNCTION audit_row();
-- messages: only creation is audited (status changes by the sender would be noise)
CREATE TRIGGER messages_audit AFTER INSERT ON messages FOR EACH ROW EXECUTE FUNCTION audit_row();

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['message_providers', 'message_templates', 'messages'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
  END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS messages, message_templates, message_providers;
ALTER TABLE companies DROP COLUMN IF EXISTS auto_send_on_payment, DROP COLUMN IF EXISTS auto_send_on_invoice,
  DROP COLUMN IF EXISTS phone_country;
