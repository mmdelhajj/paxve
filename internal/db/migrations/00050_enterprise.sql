-- Phase 8: big-company gaps.
--   * branches inside a company: a branch has its own cost center (income / expense lines of its documents are tagged
--     with it, so the branch P&L comes from the ledger), documents carry the branch (filled from the user's branch),
--     and users can be limited to some branches (row-level security hides the other branches' documents).
--   * inter-company: two companies of the organisation linked as customer / supplier of each other with a current
--     account on each side; a sale of A to B books the purchase in B automatically; loans and transfers post both
--     sides at once; the consolidated report eliminates the inter-company balances and sales.
--   * document layouts (invoice, receipt, quote, order, delivery note, PO, bill) designed without code.
--   * dashboards per user and per role from a catalogue of safe metrics.
--   * sign-in: Google / Microsoft (when configured), password policy (complexity, expiry), idle logout per company,
--     sign-in history.
--   * purchase tenders (requests to several suppliers, bid comparison, award into purchase orders) and blanket
--     agreements (price + quantity committed for a period, orders drawn against them, balance followed).

-- +goose Up

-- ======================= branches =======================
CREATE TABLE branches (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  code           text NOT NULL CHECK (code ~ '^[A-Za-z0-9_-]{1,20}$'),
  name           text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120),
  address        text NOT NULL DEFAULT '' CHECK (length(address) <= 300),
  phone          text NOT NULL DEFAULT '' CHECK (length(phone) <= 40),
  cost_center_id uuid,
  is_active      boolean NOT NULL DEFAULT true,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (company_id, code),
  UNIQUE (company_id, id),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT branches_cost_center_id_same_company FOREIGN KEY (company_id, cost_center_id) REFERENCES cost_centers (company_id, id)
);

-- the branches a user works in (empty / NULL = every branch). The first one is the default for new documents.
ALTER TABLE users ADD COLUMN branch_ids uuid[];

-- documents carry their branch
ALTER TABLE invoices ADD COLUMN branch_id uuid;
ALTER TABLE bills ADD COLUMN branch_id uuid;
ALTER TABLE payments ADD COLUMN branch_id uuid;
ALTER TABLE sales_orders ADD COLUMN branch_id uuid;
ALTER TABLE purchase_orders ADD COLUMN branch_id uuid;
ALTER TABLE expenses ADD COLUMN branch_id uuid;
ALTER TABLE supplier_payments ADD COLUMN branch_id uuid;
ALTER TABLE pos_orders ADD COLUMN branch_id uuid;
ALTER TABLE pos_registers ADD COLUMN branch_id uuid;
ALTER TABLE warehouses ADD COLUMN branch_id uuid;

-- the branches of the signed-in user (NULL = not limited)
-- +goose StatementBegin
CREATE FUNCTION app_branches() RETURNS uuid[] LANGUAGE sql STABLE PARALLEL SAFE AS $$
  SELECT CASE WHEN cardinality(u.branch_ids) > 0 THEN u.branch_ids END FROM users u WHERE u.id = app_user()
$$;
-- +goose StatementEnd

-- the default branch of the signed-in user in a company
-- +goose StatementBegin
CREATE FUNCTION default_branch(p_company uuid) RETURNS uuid LANGUAGE sql STABLE AS $$
  SELECT b.id FROM branches b, (SELECT app_branches() AS ids) u
  WHERE u.ids IS NOT NULL AND b.company_id = p_company AND b.is_active AND b.id = ANY (u.ids)
  ORDER BY array_position(u.ids, b.id) LIMIT 1
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION set_doc_branch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.branch_id IS NULL THEN
    NEW.branch_id := default_branch(NEW.company_id);
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
-- income / expense lines posted by a user of one branch get that branch's cost center (unless the line names one)
CREATE FUNCTION tag_line_branch() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  cc uuid;
BEGIN
  IF NEW.cost_center_id IS NULL AND app_branches() IS NOT NULL THEN
    SELECT b.cost_center_id INTO cc FROM branches b
      WHERE b.id = default_branch((SELECT e.company_id FROM journal_entries e WHERE e.id = NEW.entry_id));
    IF cc IS NOT NULL AND (SELECT a.type FROM accounts a WHERE a.id = NEW.account_id) IN ('income', 'expense') THEN
      NEW.cost_center_id := cc;
    END IF;
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER journal_lines_branch BEFORE INSERT ON journal_lines FOR EACH ROW EXECUTE FUNCTION tag_line_branch();

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['invoices', 'bills', 'payments', 'sales_orders', 'purchase_orders', 'expenses', 'supplier_payments',
                           'pos_orders', 'pos_registers', 'warehouses'] LOOP
    EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I FOREIGN KEY (company_id, branch_id) REFERENCES branches (company_id, id)',
                   t, t || '_branch_id_same_company');
    EXECUTE format('CREATE INDEX %I ON %I (branch_id) WHERE branch_id IS NOT NULL', t || '_branch_idx', t);
  END LOOP;
  -- documents: default branch, and a user limited to some branches sees only theirs (and the company-wide ones)
  FOREACH t IN ARRAY ARRAY['invoices', 'bills', 'payments', 'sales_orders', 'purchase_orders', 'expenses', 'supplier_payments', 'pos_orders'] LOOP
    EXECUTE format('CREATE TRIGGER %I BEFORE INSERT ON %I FOR EACH ROW EXECUTE FUNCTION set_doc_branch()', t || '_branch', t);
    EXECUTE format('CREATE POLICY branch_scope ON %I AS RESTRICTIVE FOR ALL USING ((SELECT app_branches()) IS NULL OR branch_id IS NULL OR branch_id = ANY ((SELECT app_branches())::uuid[])) WITH CHECK ((SELECT app_branches()) IS NULL OR branch_id IS NULL OR branch_id = ANY ((SELECT app_branches())::uuid[]))', t);
  END LOOP;
END
$$;
-- +goose StatementEnd

-- ======================= inter-company =======================
-- company A and company B of the organisation. Each side knows the other as a customer and as a supplier, and keeps a
-- current account (A: an asset "due from B", B: a liability "due to A"; either can go negative).
CREATE TABLE ic_links (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_a     uuid NOT NULL,
  company_b     uuid NOT NULL,
  a_customer_id uuid NOT NULL,   -- in A, the customer that is B
  a_supplier_id uuid NOT NULL,   -- in A, the supplier that is B
  b_customer_id uuid NOT NULL,   -- in B, the customer that is A
  b_supplier_id uuid NOT NULL,   -- in B, the supplier that is A
  a_account_id  uuid NOT NULL,   -- in A, the current account with B
  b_account_id  uuid NOT NULL,   -- in B, the current account with A
  auto_bills    boolean NOT NULL DEFAULT true,
  is_active     boolean NOT NULL DEFAULT true,
  since         timestamptz NOT NULL DEFAULT now(),  -- sales from this moment are mirrored
  created_by    uuid DEFAULT app_user(),
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  CHECK (company_a <> company_b),
  UNIQUE (tenant_id, company_a, company_b),
  FOREIGN KEY (tenant_id, company_a) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, company_b) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (company_a, a_customer_id) REFERENCES customers (company_id, id),
  FOREIGN KEY (company_a, a_supplier_id) REFERENCES suppliers (company_id, id),
  FOREIGN KEY (company_b, b_customer_id) REFERENCES customers (company_id, id),
  FOREIGN KEY (company_b, b_supplier_id) REFERENCES suppliers (company_id, id),
  FOREIGN KEY (company_a, a_account_id) REFERENCES accounts (company_id, id),
  FOREIGN KEY (company_b, b_account_id) REFERENCES accounts (company_id, id)
);
CREATE UNIQUE INDEX ic_links_pair_idx ON ic_links (tenant_id, least(company_a, company_b), greatest(company_a, company_b));

-- a sale of one side mirrored as a purchase of the other
CREATE TABLE ic_docs (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  link_id     uuid NOT NULL REFERENCES ic_links (id),
  src_company uuid NOT NULL,
  src_invoice uuid NOT NULL UNIQUE,
  dst_company uuid NOT NULL,
  dst_bill    uuid,
  status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'error', 'source_void')),
  error       text NOT NULL DEFAULT '',
  attempts    int NOT NULL DEFAULT 0,
  currency    char(3) NOT NULL,
  amount      numeric(20, 4) NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (src_company, src_invoice) REFERENCES invoices (company_id, id),
  FOREIGN KEY (dst_company, dst_bill) REFERENCES bills (company_id, id)
);

-- loans and transfers between the two companies: one entry on each side
CREATE TABLE ic_transfers (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  link_id       uuid NOT NULL REFERENCES ic_links (id),
  from_company  uuid NOT NULL,
  to_company    uuid NOT NULL,
  kind          text NOT NULL CHECK (kind IN ('loan', 'transfer', 'repayment')),
  transfer_date date NOT NULL,
  currency      char(3) NOT NULL REFERENCES currencies (code),
  amount        numeric(20, 4) NOT NULL CHECK (amount > 0),
  from_account  uuid NOT NULL,
  to_account    uuid NOT NULL,
  from_entry_id uuid,
  to_entry_id   uuid,
  memo          text NOT NULL DEFAULT '' CHECK (length(memo) <= 300),
  status        text NOT NULL DEFAULT 'posted' CHECK (status IN ('posted', 'void')),
  created_by    uuid DEFAULT app_user(),
  created_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (from_company, from_account) REFERENCES accounts (company_id, id),
  FOREIGN KEY (to_company, to_account) REFERENCES accounts (company_id, id)
);

-- ======================= document layouts =======================
CREATE TABLE doc_layouts (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  doc_type   text NOT NULL CHECK (doc_type IN ('invoice', 'receipt', 'quote', 'order', 'delivery', 'po', 'bill')),
  settings   jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(settings) = 'object' AND length(settings::text) <= 20000),
  updated_by uuid DEFAULT app_user(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (company_id, doc_type),
  UNIQUE (company_id, id),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id)
);

-- ======================= dashboards =======================
-- scope: 'user:<uuid>' (a user's own), 'role:<role id or built-in role>' (default of a role), 'company' (everyone)
CREATE TABLE dashboards (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  scope      text NOT NULL CHECK (scope = 'company' OR scope ~ '^(user|role):[A-Za-z0-9_-]{2,40}$'),
  widgets    jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(widgets) = 'array' AND jsonb_array_length(widgets) <= 24 AND length(widgets::text) <= 12000),
  updated_by uuid DEFAULT app_user(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (company_id, scope),
  UNIQUE (company_id, id),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id)
);

-- ======================= sign-in =======================
ALTER TABLE tenants ADD COLUMN password_complexity boolean NOT NULL DEFAULT false,
  ADD COLUMN password_expiry_days int NOT NULL DEFAULT 0 CHECK (password_expiry_days BETWEEN 0 AND 730),
  ADD COLUMN sso_allowed boolean NOT NULL DEFAULT true;
ALTER TABLE companies ADD COLUMN idle_logout_min int NOT NULL DEFAULT 0
  CHECK (idle_logout_min = 0 OR idle_logout_min BETWEEN 5 AND 1440);

GRANT UPDATE (password_complexity, password_expiry_days, sso_allowed) ON tenants TO paxve_app;
GRANT UPDATE (idle_logout_min) ON companies TO paxve_app;

CREATE TABLE login_events (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  user_id    uuid REFERENCES users (id),
  email      text NOT NULL DEFAULT '',
  at         timestamptz NOT NULL DEFAULT now(),
  ip         inet,
  user_agent text NOT NULL DEFAULT '',
  method     text NOT NULL CHECK (method IN ('password', 'mfa', 'google', 'microsoft')),
  success    boolean NOT NULL,
  reason     text NOT NULL DEFAULT ''
);
CREATE INDEX login_events_user_idx ON login_events (tenant_id, user_id, at DESC);
CREATE INDEX login_events_at_idx ON login_events (tenant_id, at DESC);

-- ======================= purchase tenders and agreements =======================
CREATE TABLE purchase_tenders (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  number     bigint NOT NULL,
  title      text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 200),
  currency   char(3) NOT NULL REFERENCES currencies (code),
  deadline   date,
  status     text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'open', 'closed', 'awarded', 'cancelled')),
  notes      text NOT NULL DEFAULT '' CHECK (length(notes) <= 2000),
  created_by uuid DEFAULT app_user(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (company_id, number),
  UNIQUE (company_id, id),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id)
);
CREATE TABLE tender_lines (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  tender_id   uuid NOT NULL,
  line_no     int NOT NULL,
  item_id     uuid,
  description text NOT NULL CHECK (length(description) <= 300),
  quantity    numeric(20, 4) NOT NULL CHECK (quantity > 0),
  UNIQUE (tender_id, line_no),
  UNIQUE (company_id, id),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (company_id, tender_id) REFERENCES purchase_tenders (company_id, id) ON DELETE CASCADE,
  FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id)
);
CREATE TABLE tender_bids (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  tender_id   uuid NOT NULL,
  supplier_id uuid NOT NULL,
  lead_days   int CHECK (lead_days IS NULL OR lead_days BETWEEN 0 AND 3650),
  valid_until date,
  notes       text NOT NULL DEFAULT '' CHECK (length(notes) <= 1000),
  status      text NOT NULL DEFAULT 'received' CHECK (status IN ('received', 'awarded', 'partial', 'lost')),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tender_id, supplier_id),
  UNIQUE (company_id, id),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (company_id, tender_id) REFERENCES purchase_tenders (company_id, id) ON DELETE CASCADE,
  FOREIGN KEY (company_id, supplier_id) REFERENCES suppliers (company_id, id)
);
CREATE TABLE tender_bid_lines (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  bid_id         uuid NOT NULL,
  tender_line_id uuid NOT NULL,
  unit_price     numeric(20, 6) NOT NULL CHECK (unit_price >= 0),
  quantity       numeric(20, 4) CHECK (quantity IS NULL OR quantity > 0),  -- NULL = the whole quantity asked
  awarded_qty    numeric(20, 4) NOT NULL DEFAULT 0,
  UNIQUE (bid_id, tender_line_id),
  UNIQUE (company_id, id),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (company_id, bid_id) REFERENCES tender_bids (company_id, id) ON DELETE CASCADE,
  FOREIGN KEY (company_id, tender_line_id) REFERENCES tender_lines (company_id, id) ON DELETE CASCADE
);
CREATE TABLE purchase_agreements (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  number      bigint NOT NULL,
  supplier_id uuid NOT NULL,
  title       text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 200),
  currency    char(3) NOT NULL REFERENCES currencies (code),
  start_date  date NOT NULL,
  end_date    date NOT NULL,
  status      text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'closed', 'cancelled')),
  notes       text NOT NULL DEFAULT '' CHECK (length(notes) <= 2000),
  tender_id   uuid,
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CHECK (end_date >= start_date),
  UNIQUE (company_id, number),
  UNIQUE (company_id, id),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (company_id, supplier_id) REFERENCES suppliers (company_id, id),
  FOREIGN KEY (company_id, tender_id) REFERENCES purchase_tenders (company_id, id)
);
CREATE TABLE purchase_agreement_lines (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  agreement_id uuid NOT NULL,
  line_no      int NOT NULL,
  item_id      uuid,
  description  text NOT NULL CHECK (length(description) <= 300),
  unit_price   numeric(20, 6) NOT NULL CHECK (unit_price >= 0),
  quantity     numeric(20, 4) NOT NULL CHECK (quantity > 0),   -- committed for the period
  UNIQUE (agreement_id, line_no),
  UNIQUE (company_id, id),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (company_id, agreement_id) REFERENCES purchase_agreements (company_id, id) ON DELETE CASCADE,
  FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id)
);
ALTER TABLE purchase_orders ADD COLUMN agreement_id uuid, ADD COLUMN tender_id uuid,
  ADD CONSTRAINT purchase_orders_agreement_id_same_company FOREIGN KEY (company_id, agreement_id) REFERENCES purchase_agreements (company_id, id),
  ADD CONSTRAINT purchase_orders_tender_id_same_company FOREIGN KEY (company_id, tender_id) REFERENCES purchase_tenders (company_id, id);
ALTER TABLE purchase_order_lines ADD COLUMN agreement_line_id uuid,
  ADD CONSTRAINT purchase_order_lines_agreement_line_id_same_company FOREIGN KEY (company_id, agreement_line_id) REFERENCES purchase_agreement_lines (company_id, id);

-- ======================= row security =======================
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['branches', 'doc_layouts', 'dashboards', 'purchase_tenders', 'tender_lines', 'tender_bids', 'tender_bid_lines',
                           'purchase_agreements', 'purchase_agreement_lines'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO paxve_app', t);
    IF t NOT IN ('tender_lines', 'tender_bid_lines', 'purchase_agreement_lines') THEN
      EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    END IF;
  END LOOP;
  -- inter-company rows belong to two companies: visible only when the request may see both (and is not inside a
  -- third company's URL)
  FOREACH t IN ARRAY ARRAY['ic_links', 'ic_docs', 'ic_transfers'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
  END LOOP;
  CREATE POLICY company_scope ON ic_links AS RESTRICTIVE FOR ALL
    USING (((SELECT app_company()) IS NULL OR (SELECT app_company()) IN (company_a, company_b))
       AND ((SELECT app_companies()) IS NULL OR (company_a = ANY ((SELECT app_companies())::uuid[]) AND company_b = ANY ((SELECT app_companies())::uuid[]))))
    WITH CHECK ((SELECT app_company()) IS NULL
       AND ((SELECT app_companies()) IS NULL OR (company_a = ANY ((SELECT app_companies())::uuid[]) AND company_b = ANY ((SELECT app_companies())::uuid[]))));
  CREATE POLICY company_scope ON ic_docs AS RESTRICTIVE FOR ALL
    USING (((SELECT app_company()) IS NULL OR (SELECT app_company()) IN (src_company, dst_company))
       AND ((SELECT app_companies()) IS NULL OR (src_company = ANY ((SELECT app_companies())::uuid[]) AND dst_company = ANY ((SELECT app_companies())::uuid[]))))
    WITH CHECK ((SELECT app_company()) IS NULL
       AND ((SELECT app_companies()) IS NULL OR (src_company = ANY ((SELECT app_companies())::uuid[]) AND dst_company = ANY ((SELECT app_companies())::uuid[]))));
  CREATE POLICY company_scope ON ic_transfers AS RESTRICTIVE FOR ALL
    USING (((SELECT app_company()) IS NULL OR (SELECT app_company()) IN (from_company, to_company))
       AND ((SELECT app_companies()) IS NULL OR (from_company = ANY ((SELECT app_companies())::uuid[]) AND to_company = ANY ((SELECT app_companies())::uuid[]))))
    WITH CHECK ((SELECT app_company()) IS NULL
       AND ((SELECT app_companies()) IS NULL OR (from_company = ANY ((SELECT app_companies())::uuid[]) AND to_company = ANY ((SELECT app_companies())::uuid[]))));
  CREATE TRIGGER ic_links_audit AFTER INSERT OR UPDATE OR DELETE ON ic_links FOR EACH ROW EXECUTE FUNCTION audit_row();
  CREATE TRIGGER ic_transfers_audit AFTER INSERT OR UPDATE OR DELETE ON ic_transfers FOR EACH ROW EXECUTE FUNCTION audit_row();

  ALTER TABLE login_events ENABLE ROW LEVEL SECURITY;
  ALTER TABLE login_events FORCE ROW LEVEL SECURITY;
  CREATE POLICY tenant_isolation ON login_events USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
  GRANT SELECT, INSERT ON login_events TO paxve_app;
END
$$;
-- +goose StatementEnd

-- ======================= issued documents may move to another branch =======================
-- (the branch is a tag like the cost center: amounts, dates and parties stay locked)
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION invoice_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'invoices cannot be deleted; void them instead' USING ERRCODE = 'PXIMM';
  END IF;
  IF OLD.status = 'void' THEN
    RAISE EXCEPTION 'a voided invoice cannot be changed' USING ERRCODE = 'PXIMM';
  END IF;
  IF (to_jsonb(NEW) - 'paid' - 'status' - 'void_entry_id' - 'notes' - 'updated_at' - 'branch_id')
     <> (to_jsonb(OLD) - 'paid' - 'status' - 'void_entry_id' - 'notes' - 'updated_at' - 'branch_id') THEN
    RAISE EXCEPTION 'issued invoices cannot be changed; void and re-issue instead' USING ERRCODE = 'PXIMM';
  END IF;
  IF NEW.status = 'void' AND OLD.paid <> 0 THEN
    RAISE EXCEPTION 'void the payments of this invoice first' USING ERRCODE = 'PXSTA';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION payment_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'payments cannot be deleted; void them instead' USING ERRCODE = 'PXIMM';
  END IF;
  IF OLD.status = 'void' THEN
    RAISE EXCEPTION 'a voided payment cannot be changed' USING ERRCODE = 'PXIMM';
  END IF;
  IF (to_jsonb(NEW) - 'status' - 'void_entry_id' - 'notes' - 'updated_at' - 'branch_id')
     <> (to_jsonb(OLD) - 'status' - 'void_entry_id' - 'notes' - 'updated_at' - 'branch_id') THEN
    RAISE EXCEPTION 'payments cannot be changed; void and re-enter instead' USING ERRCODE = 'PXIMM';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bill_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'bills cannot be deleted; void them instead' USING ERRCODE = 'PXIMM';
  END IF;
  IF OLD.status = 'void' THEN
    RAISE EXCEPTION 'a voided bill cannot be changed' USING ERRCODE = 'PXIMM';
  END IF;
  IF (to_jsonb(NEW) - 'paid' - 'status' - 'void_entry_id' - 'notes' - 'updated_at' - 'branch_id')
     <> (to_jsonb(OLD) - 'paid' - 'status' - 'void_entry_id' - 'notes' - 'updated_at' - 'branch_id') THEN
    RAISE EXCEPTION 'posted bills cannot be changed; void and re-enter instead' USING ERRCODE = 'PXIMM';
  END IF;
  IF NEW.status = 'void' AND OLD.paid <> 0 THEN
    RAISE EXCEPTION 'void the payments of this bill first' USING ERRCODE = 'PXSTA';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION posted_doc_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION '% cannot be deleted; void instead', TG_TABLE_NAME USING ERRCODE = 'PXIMM';
  END IF;
  IF OLD.status = 'void' THEN
    RAISE EXCEPTION 'a voided document cannot be changed' USING ERRCODE = 'PXIMM';
  END IF;
  IF (to_jsonb(NEW) - 'status' - 'void_entry_id' - 'notes' - 'updated_at' - 'branch_id')
     <> (to_jsonb(OLD) - 'status' - 'void_entry_id' - 'notes' - 'updated_at' - 'branch_id') THEN
    RAISE EXCEPTION 'posted documents cannot be changed; void and re-enter instead' USING ERRCODE = 'PXIMM';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
-- +goose Down
DROP TABLE IF EXISTS login_events, ic_docs, ic_transfers, ic_links, doc_layouts, dashboards CASCADE;
ALTER TABLE purchase_order_lines DROP COLUMN IF EXISTS agreement_line_id;
ALTER TABLE purchase_orders DROP COLUMN IF EXISTS agreement_id, DROP COLUMN IF EXISTS tender_id;
DROP TABLE IF EXISTS purchase_agreement_lines, purchase_agreements, tender_bid_lines, tender_bids, tender_lines, purchase_tenders CASCADE;
DROP TRIGGER IF EXISTS journal_lines_branch ON journal_lines;
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['invoices', 'bills', 'payments', 'sales_orders', 'purchase_orders', 'expenses', 'supplier_payments', 'pos_orders'] LOOP
    EXECUTE format('DROP POLICY IF EXISTS branch_scope ON %I', t);
    EXECUTE format('DROP TRIGGER IF EXISTS %I ON %I', t || '_branch', t);
  END LOOP;
  FOREACH t IN ARRAY ARRAY['invoices', 'bills', 'payments', 'sales_orders', 'purchase_orders', 'expenses', 'supplier_payments',
                           'pos_orders', 'pos_registers', 'warehouses'] LOOP
    EXECUTE format('ALTER TABLE %I DROP COLUMN IF EXISTS branch_id', t);
  END LOOP;
END
$$;
-- +goose StatementEnd
DROP FUNCTION IF EXISTS tag_line_branch(), set_doc_branch(), default_branch(uuid), app_branches();
ALTER TABLE users DROP COLUMN IF EXISTS branch_ids;
DROP TABLE IF EXISTS branches CASCADE;
ALTER TABLE tenants DROP COLUMN IF EXISTS password_complexity, DROP COLUMN IF EXISTS password_expiry_days, DROP COLUMN IF EXISTS sso_allowed;
ALTER TABLE companies DROP COLUMN IF EXISTS idle_logout_min;
