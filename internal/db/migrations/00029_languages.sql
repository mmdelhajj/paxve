-- Module T, part 3: more interface languages (3-letter codes like ckb = Kurdish Sorani), a language per
-- customer for messages and their portal page, and per-tenant corrections of any interface text.

-- +goose Up
ALTER TABLE users DROP CONSTRAINT users_locale_check;
ALTER TABLE users ADD CONSTRAINT users_locale_check CHECK (locale ~ '^[a-z]{2,3}(-[A-Z]{2})?$');

ALTER TABLE message_templates DROP CONSTRAINT IF EXISTS message_templates_locale_check;
ALTER TABLE message_templates ADD CONSTRAINT message_templates_locale_check CHECK (locale ~ '^[a-z]{2,3}$');

-- the language a customer reads (messages, portal); NULL = the company's default
ALTER TABLE customers ADD COLUMN locale text CHECK (locale ~ '^[a-z]{2,3}$');
ALTER TABLE companies ADD COLUMN default_locale text NOT NULL DEFAULT 'ar' CHECK (default_locale ~ '^[a-z]{2,3}$');

-- a tenant's own wording for interface texts (key = the translation key, e.g. "mx.j.save")
CREATE TABLE ui_translations (
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  lang       text NOT NULL CHECK (lang ~ '^[a-z]{2,3}$'),
  key        text NOT NULL CHECK (key ~ '^[A-Za-z0-9_.]{1,200}$'),
  value      text NOT NULL CHECK (length(value) <= 4000),
  updated_by uuid DEFAULT app_user(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, lang, key)
);
CREATE TRIGGER ui_translations_touch BEFORE UPDATE ON ui_translations FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
ALTER TABLE ui_translations ENABLE ROW LEVEL SECURITY;
ALTER TABLE ui_translations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ui_translations USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
GRANT SELECT, INSERT, UPDATE, DELETE ON ui_translations TO paxve_app;

-- +goose Down
DROP TABLE IF EXISTS ui_translations;
ALTER TABLE companies DROP COLUMN IF EXISTS default_locale;
ALTER TABLE customers DROP COLUMN IF EXISTS locale;
ALTER TABLE message_templates DROP CONSTRAINT IF EXISTS message_templates_locale_check;
ALTER TABLE message_templates ADD CONSTRAINT message_templates_locale_check CHECK (locale ~ '^[a-z]{2}$');
ALTER TABLE users DROP CONSTRAINT users_locale_check;
ALTER TABLE users ADD CONSTRAINT users_locale_check CHECK (locale ~ '^[a-z]{2}(-[A-Z]{2})?$');
