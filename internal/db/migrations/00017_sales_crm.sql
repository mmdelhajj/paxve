-- Module M: sales (catalog, quotations -> sales orders -> deliveries / invoices), customer statements, CRM
-- (pipeline, activities, notes), company document settings (logo, header, footer) and e-mail (SMTP) sending.
-- Also: realized FX on supplier allocations, stock coming back on customer credit notes.

-- +goose Up

-- ---- supplier allocations book the FX difference like customer allocations ----
ALTER TABLE supplier_allocations
  ADD COLUMN fx_entry_id uuid,
  ADD FOREIGN KEY (tenant_id, fx_entry_id) REFERENCES journal_entries (tenant_id, id);

ALTER TABLE stock_moves DROP CONSTRAINT IF EXISTS stock_moves_source_check;
ALTER TABLE stock_moves ADD CONSTRAINT stock_moves_source_check CHECK (source IN ('bill', 'bill_void', 'fuel', 'adjust', 'sale', 'sale_void',
  'stock_void', 'transfer', 'transfer_void', 'debit_note', 'debit_note_void', 'credit_note', 'credit_note_void', 'delivery', 'delivery_void'));

-- ---- product / service catalog ----
ALTER TABLE items
  ADD COLUMN is_sellable boolean NOT NULL DEFAULT true,
  ADD COLUMN is_purchasable boolean NOT NULL DEFAULT true,
  ADD COLUMN sale_tax_rate_id uuid,
  ADD COLUMN description text NOT NULL DEFAULT '',
  ADD FOREIGN KEY (tenant_id, sale_tax_rate_id) REFERENCES tax_rates (tenant_id, id);

-- sale price per currency (the item's main price stays items.sale_price / sale_currency)
CREATE TABLE item_prices (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  item_id    uuid NOT NULL,
  currency   char(3) NOT NULL REFERENCES currencies (code),
  price      numeric(20, 4) NOT NULL CHECK (price >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, item_id) REFERENCES items (tenant_id, id),
  UNIQUE (tenant_id, item_id, currency)
);

-- ---- company documents ----
ALTER TABLE companies
  ADD COLUMN logo text NOT NULL DEFAULT '' CHECK (logo = '' OR (logo LIKE 'data:image/%' AND length(logo) <= 400000)),
  ADD COLUMN doc_header text NOT NULL DEFAULT '' CHECK (length(doc_header) <= 2000),
  ADD COLUMN doc_footer text NOT NULL DEFAULT '' CHECK (length(doc_footer) <= 2000),
  ADD COLUMN doc_terms text NOT NULL DEFAULT '' CHECK (length(doc_terms) <= 4000),
  ADD COLUMN tax_id text NOT NULL DEFAULT '',
  ADD COLUMN quote_valid_days int NOT NULL DEFAULT 30 CHECK (quote_valid_days BETWEEN 1 AND 365);

-- ---- CRM ----
CREATE TABLE crm_stages (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) > 0),
  name_i18n  jsonb NOT NULL DEFAULT '{}'::jsonb,
  seq        int NOT NULL DEFAULT 0,
  probability int NOT NULL DEFAULT 10 CHECK (probability BETWEEN 0 AND 100),
  is_won     boolean NOT NULL DEFAULT false,
  is_active  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, id)
);

CREATE TABLE crm_leads (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  title            text NOT NULL CHECK (length(trim(title)) > 0),
  contact_name     text NOT NULL DEFAULT '',
  organisation     text NOT NULL DEFAULT '',
  phone            text NOT NULL DEFAULT '',
  email            text NOT NULL DEFAULT '',
  address          text NOT NULL DEFAULT '',
  source           text NOT NULL DEFAULT '',
  stage_id         uuid,
  status           text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'won', 'lost')),
  lost_reason      text NOT NULL DEFAULT '',
  expected_revenue numeric(20, 4) NOT NULL DEFAULT 0 CHECK (expected_revenue >= 0),
  currency         char(3) REFERENCES currencies (code),
  probability      int NOT NULL DEFAULT 10 CHECK (probability BETWEEN 0 AND 100),
  expected_close   date,
  assigned_to      uuid,
  customer_id      uuid,
  notes            text NOT NULL DEFAULT '',
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  closed_at        timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, stage_id) REFERENCES crm_stages (tenant_id, id),
  FOREIGN KEY (tenant_id, assigned_to) REFERENCES users (tenant_id, id),
  FOREIGN KEY (tenant_id, customer_id) REFERENCES customers (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, number)
);
CREATE INDEX crm_leads_stage_idx ON crm_leads (tenant_id, company_id, status, stage_id);
CREATE INDEX crm_leads_customer_idx ON crm_leads (tenant_id, customer_id) WHERE customer_id IS NOT NULL;

-- activities (call / meeting / task / …) and notes, on a lead and/or a customer
CREATE TABLE crm_activities (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  lead_id     uuid,
  customer_id uuid,
  kind        text NOT NULL DEFAULT 'task' CHECK (kind IN ('note', 'call', 'meeting', 'task', 'whatsapp', 'email', 'visit')),
  summary     text NOT NULL CHECK (length(trim(summary)) > 0),
  details     text NOT NULL DEFAULT '',
  due_date    date,
  assigned_to uuid,
  done_at     timestamptz,
  done_by     uuid,
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, lead_id) REFERENCES crm_leads (tenant_id, id),
  FOREIGN KEY (tenant_id, customer_id) REFERENCES customers (tenant_id, id),
  FOREIGN KEY (tenant_id, assigned_to) REFERENCES users (tenant_id, id),
  CHECK (lead_id IS NOT NULL OR customer_id IS NOT NULL)
);
CREATE INDEX crm_activities_open_idx ON crm_activities (tenant_id, company_id, due_date) WHERE done_at IS NULL AND kind <> 'note';
CREATE INDEX crm_activities_lead_idx ON crm_activities (tenant_id, lead_id);
CREATE INDEX crm_activities_customer_idx ON crm_activities (tenant_id, customer_id);

-- ---- quotations / sales orders ----
-- One document goes draft -> sent (quotation) -> confirmed (sales order) -> done, or cancelled.
CREATE TABLE sales_orders (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,              -- quotation number
  order_number     bigint,                       -- given when confirmed
  customer_id      uuid NOT NULL,
  lead_id          uuid,
  currency         char(3) NOT NULL REFERENCES currencies (code),
  quote_date       date NOT NULL,
  valid_until      date,
  order_date       date,
  status           text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'sent', 'confirmed', 'done', 'cancelled')),
  subtotal         numeric(20, 4) NOT NULL DEFAULT 0,
  tax_total        numeric(20, 4) NOT NULL DEFAULT 0,
  total            numeric(20, 4) NOT NULL DEFAULT 0,
  revenue_account_id uuid,
  salesperson_id   uuid,
  notes            text NOT NULL DEFAULT '',
  terms            text NOT NULL DEFAULT '',
  accepted_at      timestamptz,                  -- accepted by the customer on the portal
  accepted_name    text NOT NULL DEFAULT '',
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, customer_id) REFERENCES customers (tenant_id, id),
  FOREIGN KEY (tenant_id, lead_id) REFERENCES crm_leads (tenant_id, id),
  FOREIGN KEY (tenant_id, salesperson_id) REFERENCES users (tenant_id, id),
  FOREIGN KEY (tenant_id, revenue_account_id) REFERENCES accounts (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, number),
  UNIQUE (tenant_id, company_id, order_number)
);
CREATE INDEX sales_orders_customer_idx ON sales_orders (tenant_id, customer_id);
CREATE INDEX sales_orders_status_idx ON sales_orders (tenant_id, company_id, status, quote_date DESC);

CREATE TABLE sales_order_lines (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  order_id      uuid NOT NULL,
  line_no       int NOT NULL,
  item_id       uuid,
  description   text NOT NULL CHECK (length(trim(description)) > 0),
  quantity      numeric(14, 4) NOT NULL CHECK (quantity > 0),
  unit_price    numeric(20, 4) NOT NULL CHECK (unit_price >= 0),
  discount_pct  numeric(7, 4) NOT NULL DEFAULT 0 CHECK (discount_pct >= 0 AND discount_pct <= 100),
  amount        numeric(20, 4) NOT NULL CHECK (amount >= 0),  -- after discount, before tax
  tax_rate_id   uuid,
  tax_amount    numeric(20, 4) NOT NULL DEFAULT 0 CHECK (tax_amount >= 0),
  warehouse_id  uuid,
  qty_delivered numeric(14, 4) NOT NULL DEFAULT 0 CHECK (qty_delivered >= 0),
  qty_invoiced  numeric(14, 4) NOT NULL DEFAULT 0 CHECK (qty_invoiced >= 0),
  FOREIGN KEY (tenant_id, order_id) REFERENCES sales_orders (tenant_id, id),
  FOREIGN KEY (tenant_id, item_id) REFERENCES items (tenant_id, id),
  FOREIGN KEY (tenant_id, tax_rate_id) REFERENCES tax_rates (tenant_id, id),
  FOREIGN KEY (tenant_id, warehouse_id) REFERENCES warehouses (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, order_id, line_no),
  CHECK (qty_delivered <= quantity AND qty_invoiced <= quantity)
);

-- deliveries: goods leave stock (Dr cost of sales / Cr inventory); the moves are stock_moves source 'delivery'
CREATE TABLE sales_deliveries (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  order_id         uuid NOT NULL,
  delivery_date    date NOT NULL,
  lines            jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{line_id, quantity}]
  status           text NOT NULL DEFAULT 'posted' CHECK (status IN ('posted', 'void')),
  journal_entry_id uuid,
  void_entry_id    uuid,
  notes            text NOT NULL DEFAULT '',
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, order_id) REFERENCES sales_orders (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  FOREIGN KEY (tenant_id, void_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (tenant_id, company_id, number)
);
CREATE INDEX sales_deliveries_order_idx ON sales_deliveries (tenant_id, order_id);

ALTER TABLE invoices
  ADD COLUMN sales_order_id uuid,
  ADD FOREIGN KEY (tenant_id, sales_order_id) REFERENCES sales_orders (tenant_id, id);
CREATE INDEX invoices_sales_order_idx ON invoices (tenant_id, sales_order_id) WHERE sales_order_id IS NOT NULL;
-- which order lines an invoice line bills (quantities are given back when the invoice is voided)
ALTER TABLE invoice_lines
  ADD COLUMN order_line_id uuid,
  ADD FOREIGN KEY (tenant_id, order_line_id) REFERENCES sales_order_lines (tenant_id, id);

-- ---- e-mail ----
ALTER TABLE message_providers DROP CONSTRAINT IF EXISTS message_providers_kind_check;
ALTER TABLE message_providers ADD CONSTRAINT message_providers_kind_check
  CHECK (kind IN ('whatsapp_cloud', 'twilio_sms', 'generic_http', 'log', 'smtp'));
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_channel_check;
ALTER TABLE messages ADD CONSTRAINT messages_channel_check CHECK (channel IN ('whatsapp', 'sms', 'email'));
ALTER TABLE messages
  ADD COLUMN email text NOT NULL DEFAULT '',
  ADD COLUMN subject text NOT NULL DEFAULT '';

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['item_prices', 'crm_stages', 'crm_leads', 'crm_activities', 'sales_orders', 'sales_order_lines', 'sales_deliveries'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
    EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
  END LOOP;
  FOREACH t IN ARRAY ARRAY['item_prices', 'crm_stages', 'crm_leads', 'crm_activities', 'sales_orders', 'sales_deliveries'] LOOP
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION touch_updated_at()', t || '_touch', t);
  END LOOP;
END
$$;
-- +goose StatementEnd
-- prices, stages and order lines are replaced as a whole; activities can be deleted
GRANT DELETE ON item_prices, sales_order_lines, crm_activities TO paxve_app;
CREATE TRIGGER sales_deliveries_guard BEFORE UPDATE OR DELETE ON sales_deliveries FOR EACH ROW EXECUTE FUNCTION posted_doc_guard();

-- +goose Down
DROP TRIGGER IF EXISTS sales_deliveries_guard ON sales_deliveries;
ALTER TABLE messages DROP COLUMN IF EXISTS email, DROP COLUMN IF EXISTS subject;
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_channel_check;
ALTER TABLE messages ADD CONSTRAINT messages_channel_check CHECK (channel IN ('whatsapp', 'sms'));
ALTER TABLE message_providers DROP CONSTRAINT IF EXISTS message_providers_kind_check;
ALTER TABLE message_providers ADD CONSTRAINT message_providers_kind_check CHECK (kind IN ('whatsapp_cloud', 'twilio_sms', 'generic_http', 'log'));
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS order_line_id;
ALTER TABLE invoices DROP COLUMN IF EXISTS sales_order_id;
DROP TABLE IF EXISTS sales_deliveries, sales_order_lines, sales_orders, crm_activities, crm_leads, crm_stages, item_prices;
ALTER TABLE companies DROP COLUMN IF EXISTS logo, DROP COLUMN IF EXISTS doc_header, DROP COLUMN IF EXISTS doc_footer,
  DROP COLUMN IF EXISTS doc_terms, DROP COLUMN IF EXISTS tax_id, DROP COLUMN IF EXISTS quote_valid_days;
ALTER TABLE items DROP COLUMN IF EXISTS is_sellable, DROP COLUMN IF EXISTS is_purchasable, DROP COLUMN IF EXISTS sale_tax_rate_id,
  DROP COLUMN IF EXISTS description;
ALTER TABLE supplier_allocations DROP COLUMN IF EXISTS fx_entry_id;
