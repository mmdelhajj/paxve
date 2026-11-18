-- Collectors: a collector role, personal cash boxes, offline-safe payments, cash transfers (handover).

-- +goose Up

ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('admin', 'accountant', 'data_entry', 'read_only', 'collector'));
ALTER TABLE api_keys DROP CONSTRAINT api_keys_role_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_role_check CHECK (role IN ('admin', 'accountant', 'data_entry', 'read_only', 'collector'));

-- A payment method can belong to one person (the collector's own cash box).
ALTER TABLE payment_methods ADD COLUMN user_id uuid;
ALTER TABLE payment_methods ADD FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id);

-- Offline phones send a client reference; the same payment sent twice is stored once.
ALTER TABLE payments ADD COLUMN client_ref text CHECK (client_ref IS NULL OR length(client_ref) BETWEEN 8 AND 64);
CREATE UNIQUE INDEX payments_client_ref_key ON payments (tenant_id, client_ref) WHERE client_ref IS NOT NULL;
CREATE INDEX payments_received_by_idx ON payments (tenant_id, company_id, received_by, payment_date);

-- Moving money between boxes (collector hands cash to the office, cash to bank...).
CREATE TABLE cash_transfers (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  transfer_date    date NOT NULL,
  from_method_id   uuid NOT NULL,
  to_method_id     uuid NOT NULL,
  currency         char(3) NOT NULL REFERENCES currencies (code),
  amount           numeric(20, 4) NOT NULL CHECK (amount > 0),
  journal_entry_id uuid NOT NULL,
  notes            text NOT NULL DEFAULT '',
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, from_method_id) REFERENCES payment_methods (tenant_id, id),
  FOREIGN KEY (tenant_id, to_method_id) REFERENCES payment_methods (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (tenant_id, company_id, number),
  CHECK (from_method_id <> to_method_id)
);
CREATE TRIGGER cash_transfers_immutable BEFORE UPDATE OR DELETE ON cash_transfers FOR EACH ROW EXECUTE FUNCTION forbid_change();
CREATE TRIGGER cash_transfers_audit AFTER INSERT ON cash_transfers FOR EACH ROW EXECUTE FUNCTION audit_row();
ALTER TABLE cash_transfers ENABLE ROW LEVEL SECURITY;
ALTER TABLE cash_transfers FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON cash_transfers USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
GRANT SELECT, INSERT ON cash_transfers TO paxve_app;

-- +goose Down
DROP TABLE IF EXISTS cash_transfers;
DROP INDEX IF EXISTS payments_client_ref_key;
ALTER TABLE payments DROP COLUMN IF EXISTS client_ref;
ALTER TABLE payment_methods DROP COLUMN IF EXISTS user_id;
