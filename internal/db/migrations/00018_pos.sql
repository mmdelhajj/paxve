-- Module N: point of sale (registers, cashier sessions, POS orders), item barcodes / categories / images,
-- a cashier role, and credit notes per invoice line (only the goods of the lines credited go back to stock).

-- +goose Up

-- ---- invoice lines remember what they sold (so a credit note can return exactly those lines) ----
ALTER TABLE invoice_lines
  ADD COLUMN item_id uuid,
  ADD COLUMN warehouse_id uuid,
  ADD COLUMN revenue_account_id uuid,
  ADD FOREIGN KEY (tenant_id, item_id) REFERENCES items (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, warehouse_id) REFERENCES warehouses (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, revenue_account_id) REFERENCES accounts (tenant_id, id);

-- which invoice lines (and how much of them) a credit note credits
CREATE TABLE credit_note_lines (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  payment_id      uuid NOT NULL,                      -- the credit note (payments.kind = 'credit_note')
  invoice_line_id uuid NOT NULL REFERENCES invoice_lines (id),
  quantity        numeric(14, 4) NOT NULL CHECK (quantity > 0),
  amount          numeric(20, 4) NOT NULL CHECK (amount >= 0),     -- before tax
  tax_amount      numeric(20, 4) NOT NULL DEFAULT 0 CHECK (tax_amount >= 0),
  FOREIGN KEY (tenant_id, payment_id) REFERENCES payments (tenant_id, id)
);
CREATE INDEX credit_note_lines_line_idx ON credit_note_lines (tenant_id, invoice_line_id);
CREATE INDEX credit_note_lines_payment_idx ON credit_note_lines (tenant_id, payment_id);

-- ---- cashier role ----
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('admin', 'accountant', 'data_entry', 'read_only', 'collector', 'cashier'));

-- ---- catalog: categories, barcodes, images ----
CREATE TABLE item_categories (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) > 0),
  name_i18n  jsonb NOT NULL DEFAULT '{}'::jsonb,
  color      text NOT NULL DEFAULT 'teal' CHECK (color ~ '^[a-z]{2,12}$'),
  seq        int NOT NULL DEFAULT 0,
  is_active  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, id)
);

ALTER TABLE items
  ADD COLUMN barcode text NOT NULL DEFAULT '' CHECK (length(barcode) <= 64),
  ADD COLUMN category_id uuid,
  ADD COLUMN image text NOT NULL DEFAULT '' CHECK (image = '' OR (image LIKE 'data:image/%' AND length(image) <= 120000)),
  ADD COLUMN pos_visible boolean NOT NULL DEFAULT true,
  ADD FOREIGN KEY (tenant_id, category_id) REFERENCES item_categories (tenant_id, id);
CREATE UNIQUE INDEX items_barcode_uniq ON items (tenant_id, company_id, barcode) WHERE barcode <> '';

-- ---- registers (tills) ----
CREATE TABLE pos_registers (
  id                    uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id             uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id            uuid NOT NULL,
  name                  text NOT NULL CHECK (length(trim(name)) > 0),
  currency              char(3) NOT NULL REFERENCES currencies (code),   -- prices are shown and invoiced in this currency
  warehouse_id          uuid NOT NULL,
  customer_id           uuid NOT NULL,                                   -- the walk-in customer
  method_ids            uuid[] NOT NULL DEFAULT '{}',                    -- cash boxes / methods the till takes
  revenue_account_id    uuid,                                            -- "" = item / company revenue account
  over_short_account_id uuid NOT NULL,
  cashier_refunds       boolean NOT NULL DEFAULT false,                  -- cashiers may refund (else only office roles)
  cashier_price_edit    boolean NOT NULL DEFAULT false,                  -- cashiers may change prices
  receipt_footer        text NOT NULL DEFAULT '' CHECK (length(receipt_footer) <= 500),
  is_active             boolean NOT NULL DEFAULT true,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, warehouse_id) REFERENCES warehouses (tenant_id, id),
  FOREIGN KEY (tenant_id, customer_id) REFERENCES customers (tenant_id, id),
  FOREIGN KEY (tenant_id, revenue_account_id) REFERENCES accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, over_short_account_id) REFERENCES accounts (tenant_id, id),
  UNIQUE (tenant_id, id)
);

-- ---- sessions (one cashier shift on one register) ----
CREATE TABLE pos_sessions (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id      uuid NOT NULL,
  register_id     uuid NOT NULL,
  number          bigint NOT NULL,
  status          text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  opened_by       uuid DEFAULT app_user(),
  opened_at       timestamptz NOT NULL DEFAULT now(),
  opening         jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{method_id, amount}] float in the drawer
  closed_by       uuid,
  closed_at       timestamptz,
  counted         jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{method_id, currency, expected, counted, difference}]
  over_short_entry_id uuid,
  notes           text NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, register_id) REFERENCES pos_registers (tenant_id, id),
  FOREIGN KEY (tenant_id, opened_by) REFERENCES users (tenant_id, id),
  FOREIGN KEY (tenant_id, closed_by) REFERENCES users (tenant_id, id),
  FOREIGN KEY (tenant_id, over_short_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, number)
);
CREATE UNIQUE INDEX pos_sessions_one_open ON pos_sessions (tenant_id, register_id) WHERE status = 'open';

-- ---- orders: each sale is a customer invoice (+ the payment taken at the till); a return is a credit note ----
CREATE TABLE pos_orders (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  session_id     uuid NOT NULL,
  register_id    uuid NOT NULL,
  number         bigint NOT NULL,
  kind           text NOT NULL DEFAULT 'sale' CHECK (kind IN ('sale', 'refund')),
  order_date     date NOT NULL,
  customer_id    uuid NOT NULL,
  currency       char(3) NOT NULL REFERENCES currencies (code),
  total          numeric(20, 4) NOT NULL,             -- incl. tax (refund: amount credited)
  tax_total      numeric(20, 4) NOT NULL DEFAULT 0,
  paid           numeric(20, 4) NOT NULL DEFAULT 0,   -- applied by the till payment (sale) / paid back (refund)
  invoice_id     uuid,                                -- sale: the invoice
  payment_id     uuid,                                -- sale: the till payment; refund: the credit note
  refund_doc_id  uuid,                                -- refund: the refund document (money paid back)
  refund_of      uuid,                                -- refund: the sale
  cash           jsonb NOT NULL DEFAULT '[]'::jsonb,  -- [{method_id, currency, amount}] money in (+) / out (-) of the drawer
  client_ref     text,
  client_time    timestamptz,
  cashier_id     uuid DEFAULT app_user(),
  notes          text NOT NULL DEFAULT '',
  created_at     timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, session_id) REFERENCES pos_sessions (tenant_id, id),
  FOREIGN KEY (tenant_id, register_id) REFERENCES pos_registers (tenant_id, id),
  FOREIGN KEY (tenant_id, customer_id) REFERENCES customers (tenant_id, id),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoices (tenant_id, id),
  FOREIGN KEY (tenant_id, payment_id) REFERENCES payments (tenant_id, id),
  FOREIGN KEY (tenant_id, refund_doc_id) REFERENCES invoices (tenant_id, id),
  FOREIGN KEY (tenant_id, refund_of) REFERENCES pos_orders (tenant_id, id),
  FOREIGN KEY (tenant_id, cashier_id) REFERENCES users (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, number),
  UNIQUE (tenant_id, company_id, client_ref)
);
CREATE INDEX pos_orders_session_idx ON pos_orders (tenant_id, session_id, created_at);
CREATE INDEX pos_orders_date_idx ON pos_orders (tenant_id, company_id, order_date);
CREATE INDEX pos_orders_invoice_idx ON pos_orders (tenant_id, invoice_id) WHERE invoice_id IS NOT NULL;

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['credit_note_lines', 'item_categories', 'pos_registers', 'pos_sessions', 'pos_orders'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
    EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
  END LOOP;
  FOREACH t IN ARRAY ARRAY['item_categories', 'pos_registers', 'pos_sessions'] LOOP
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION touch_updated_at()', t || '_touch', t);
  END LOOP;
END
$$;
-- +goose StatementEnd
-- credit note lines and POS orders are written once
CREATE TRIGGER credit_note_lines_immutable BEFORE UPDATE OR DELETE ON credit_note_lines FOR EACH ROW EXECUTE FUNCTION forbid_change();
CREATE TRIGGER pos_orders_immutable BEFORE UPDATE OR DELETE ON pos_orders FOR EACH ROW EXECUTE FUNCTION forbid_change();

-- +goose Down
DROP TABLE IF EXISTS pos_orders, pos_sessions, pos_registers, credit_note_lines;
DROP INDEX IF EXISTS items_barcode_uniq;
ALTER TABLE items DROP COLUMN IF EXISTS barcode, DROP COLUMN IF EXISTS category_id, DROP COLUMN IF EXISTS image, DROP COLUMN IF EXISTS pos_visible;
DROP TABLE IF EXISTS item_categories;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('admin', 'accountant', 'data_entry', 'read_only', 'collector'));
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS item_id, DROP COLUMN IF EXISTS warehouse_id, DROP COLUMN IF EXISTS revenue_account_id;
