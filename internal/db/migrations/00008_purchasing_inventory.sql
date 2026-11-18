-- Suppliers, purchase bills, supplier payments, expenses, items, warehouses, stock (weighted average cost).
-- Rules enforced here:
--   * stock quantity can never go negative
--   * a bill can never be paid more than its total
--   * posted documents (bills, payments, expenses, stock moves) are never changed or deleted; they are voided (reversed)

-- +goose Up

ALTER TABLE companies
  ADD COLUMN ap_account_id uuid,
  ADD COLUMN inventory_account_id uuid,
  ADD COLUMN cogs_account_id uuid,
  ADD COLUMN fuel_account_id uuid,
  ADD COLUMN stock_adjust_account_id uuid,
  ADD COLUMN default_expense_account_id uuid,
  ADD FOREIGN KEY (tenant_id, ap_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, inventory_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, cogs_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, fuel_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, stock_adjust_account_id) REFERENCES accounts (tenant_id, id),
  ADD FOREIGN KEY (tenant_id, default_expense_account_id) REFERENCES accounts (tenant_id, id);

-- Backfill defaults for companies that already loaded a template (by account code).
UPDATE companies c SET
  ap_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('401', '2100') ORDER BY a.code LIMIT 1),
  inventory_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('31', '1400') ORDER BY a.code LIMIT 1),
  cogs_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('607', '5100') ORDER BY a.code LIMIT 1),
  fuel_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('601', '5100') ORDER BY a.code LIMIT 1),
  stock_adjust_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('602', '6900') ORDER BY a.code LIMIT 1),
  default_expense_account_id = (SELECT a.id FROM accounts a WHERE a.company_id = c.id AND a.code IN ('602', '6900') ORDER BY a.code LIMIT 1);

CREATE TABLE suppliers (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  code       text NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) > 0),
  phone      text NOT NULL DEFAULT '',
  email      text NOT NULL DEFAULT '',
  address    text NOT NULL DEFAULT '',
  tax_id     text NOT NULL DEFAULT '',
  currency   char(3) REFERENCES currencies (code),
  notes      text NOT NULL DEFAULT '',
  status     text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, code)
);

CREATE TABLE items (
  id                   uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id            uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id           uuid NOT NULL,
  code                 text NOT NULL,
  name                 text NOT NULL CHECK (length(trim(name)) > 0),
  name_i18n            jsonb NOT NULL DEFAULT '{}'::jsonb,
  kind                 text NOT NULL DEFAULT 'stock' CHECK (kind IN ('stock', 'service', 'consumable')),
  unit                 text NOT NULL DEFAULT 'pcs',
  sale_price           numeric(20, 4) NOT NULL DEFAULT 0 CHECK (sale_price >= 0),
  sale_currency        char(3) REFERENCES currencies (code),
  inventory_account_id uuid,
  expense_account_id   uuid,
  revenue_account_id   uuid,
  is_active            boolean NOT NULL DEFAULT true,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, inventory_account_id) REFERENCES accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, expense_account_id) REFERENCES accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, revenue_account_id) REFERENCES accounts (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, code),
  CHECK (kind <> 'stock' OR inventory_account_id IS NOT NULL)
);

CREATE TABLE warehouses (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) > 0),
  is_active  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, name)
);

-- Current stock per item and warehouse. avg_cost is in the company base currency.
CREATE TABLE stock (
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  item_id      uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  quantity     numeric(18, 4) NOT NULL DEFAULT 0 CHECK (quantity >= 0),
  avg_cost     numeric(20, 6) NOT NULL DEFAULT 0 CHECK (avg_cost >= 0),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, item_id, warehouse_id),
  FOREIGN KEY (tenant_id, item_id) REFERENCES items (tenant_id, id),
  FOREIGN KEY (tenant_id, warehouse_id) REFERENCES warehouses (tenant_id, id)
);

CREATE TABLE stock_moves (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  item_id          uuid NOT NULL,
  warehouse_id     uuid NOT NULL,
  move_date        date NOT NULL,
  quantity         numeric(18, 4) NOT NULL CHECK (quantity <> 0),
  unit_cost        numeric(20, 6) NOT NULL,
  value            numeric(20, 4) NOT NULL,
  source           text NOT NULL CHECK (source IN ('bill', 'bill_void', 'fuel', 'adjust', 'sale', 'sale_void')),
  source_id        uuid,
  journal_entry_id uuid,
  notes            text NOT NULL DEFAULT '',
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, item_id) REFERENCES items (tenant_id, id),
  FOREIGN KEY (tenant_id, warehouse_id) REFERENCES warehouses (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id)
);
CREATE INDEX stock_moves_item_idx ON stock_moves (tenant_id, item_id, move_date, created_at);
CREATE INDEX stock_moves_source_idx ON stock_moves (tenant_id, source, source_id);

CREATE TABLE bills (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  supplier_id      uuid NOT NULL,
  supplier_ref     text NOT NULL DEFAULT '',
  bill_date        date NOT NULL,
  due_date         date NOT NULL,
  currency         char(3) NOT NULL REFERENCES currencies (code),
  total            numeric(20, 4) NOT NULL CHECK (total > 0),
  paid             numeric(20, 4) NOT NULL DEFAULT 0,
  status           text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'partial', 'paid', 'void')),
  journal_entry_id uuid,
  void_entry_id    uuid,
  notes            text NOT NULL DEFAULT '',
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, supplier_id) REFERENCES suppliers (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  FOREIGN KEY (tenant_id, void_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, number),
  CHECK (paid >= 0 AND paid <= total),
  CHECK (due_date >= bill_date)
);
CREATE INDEX bills_supplier_idx ON bills (tenant_id, supplier_id, bill_date);

CREATE TABLE bill_lines (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  bill_id      uuid NOT NULL,
  line_no      int NOT NULL,
  item_id      uuid,
  warehouse_id uuid,
  account_id   uuid NOT NULL,
  description  text NOT NULL DEFAULT '',
  quantity     numeric(18, 4) NOT NULL CHECK (quantity > 0),
  unit_price   numeric(20, 4) NOT NULL CHECK (unit_price >= 0),
  amount       numeric(20, 4) NOT NULL CHECK (amount >= 0),
  FOREIGN KEY (tenant_id, bill_id) REFERENCES bills (tenant_id, id),
  FOREIGN KEY (tenant_id, item_id) REFERENCES items (tenant_id, id),
  FOREIGN KEY (tenant_id, warehouse_id) REFERENCES warehouses (tenant_id, id),
  FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id),
  UNIQUE (tenant_id, bill_id, line_no)
);

CREATE TABLE supplier_payments (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  supplier_id      uuid NOT NULL,
  payment_date     date NOT NULL,
  currency         char(3) NOT NULL REFERENCES currencies (code),
  amount           numeric(20, 4) NOT NULL CHECK (amount > 0),
  method_id        uuid NOT NULL,
  paid_currency    char(3) NOT NULL REFERENCES currencies (code),
  paid_amount      numeric(20, 4) NOT NULL CHECK (paid_amount > 0),
  paid_rate        numeric(28, 10) NOT NULL CHECK (paid_rate > 0),
  status           text NOT NULL DEFAULT 'posted' CHECK (status IN ('posted', 'void')),
  journal_entry_id uuid,
  void_entry_id    uuid,
  notes            text NOT NULL DEFAULT '',
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, supplier_id) REFERENCES suppliers (tenant_id, id),
  FOREIGN KEY (tenant_id, method_id) REFERENCES payment_methods (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  FOREIGN KEY (tenant_id, void_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, number)
);

CREATE TABLE supplier_allocations (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  payment_id uuid NOT NULL,
  bill_id    uuid NOT NULL,
  amount     numeric(20, 4) NOT NULL CHECK (amount <> 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, payment_id) REFERENCES supplier_payments (tenant_id, id),
  FOREIGN KEY (tenant_id, bill_id) REFERENCES bills (tenant_id, id)
);
CREATE INDEX supplier_allocations_bill_idx ON supplier_allocations (tenant_id, bill_id);

CREATE TABLE expenses (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  expense_date     date NOT NULL,
  account_id       uuid NOT NULL,
  method_id        uuid NOT NULL,
  currency         char(3) NOT NULL REFERENCES currencies (code),
  amount           numeric(20, 4) NOT NULL CHECK (amount > 0),
  description      text NOT NULL DEFAULT '',
  supplier_id      uuid,
  status           text NOT NULL DEFAULT 'posted' CHECK (status IN ('posted', 'void')),
  journal_entry_id uuid,
  void_entry_id    uuid,
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, method_id) REFERENCES payment_methods (tenant_id, id),
  FOREIGN KEY (tenant_id, supplier_id) REFERENCES suppliers (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  FOREIGN KEY (tenant_id, void_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, company_id, number)
);
CREATE INDEX expenses_date_idx ON expenses (tenant_id, company_id, expense_date);

-- Fuel consumption and stock adjustments (one item per document).
CREATE TABLE stock_docs (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  number           bigint NOT NULL,
  kind             text NOT NULL CHECK (kind IN ('fuel', 'adjust')),
  doc_date         date NOT NULL,
  item_id          uuid NOT NULL,
  warehouse_id     uuid NOT NULL,
  quantity         numeric(18, 4) NOT NULL CHECK (quantity <> 0),
  value            numeric(20, 4) NOT NULL,
  account_id       uuid NOT NULL,
  notes            text NOT NULL DEFAULT '',
  journal_entry_id uuid,
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, item_id) REFERENCES items (tenant_id, id),
  FOREIGN KEY (tenant_id, warehouse_id) REFERENCES warehouses (tenant_id, id),
  FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (tenant_id, company_id, kind, number)
);

-- Keep bills.paid and status in step with supplier allocations.
-- +goose StatementBegin
CREATE FUNCTION apply_bill_allocation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  UPDATE bills
  SET paid = paid + NEW.amount,
      status = CASE
        WHEN status = 'void' THEN 'void'
        WHEN paid + NEW.amount = 0 THEN 'open'
        WHEN paid + NEW.amount >= total THEN 'paid'
        ELSE 'partial' END
  WHERE id = NEW.bill_id;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER supplier_allocations_apply AFTER INSERT ON supplier_allocations
  FOR EACH ROW EXECUTE FUNCTION apply_bill_allocation();

-- +goose StatementBegin
CREATE FUNCTION bill_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'bills cannot be deleted; void them instead' USING ERRCODE = 'PXIMM';
  END IF;
  IF OLD.status = 'void' THEN
    RAISE EXCEPTION 'a voided bill cannot be changed' USING ERRCODE = 'PXIMM';
  END IF;
  IF (to_jsonb(NEW) - 'paid' - 'status' - 'void_entry_id' - 'notes' - 'updated_at')
     <> (to_jsonb(OLD) - 'paid' - 'status' - 'void_entry_id' - 'notes' - 'updated_at') THEN
    RAISE EXCEPTION 'posted bills cannot be changed; void and re-enter instead' USING ERRCODE = 'PXIMM';
  END IF;
  IF NEW.status = 'void' AND OLD.paid <> 0 THEN
    RAISE EXCEPTION 'void the payments of this bill first' USING ERRCODE = 'PXSTA';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER bills_guard BEFORE UPDATE OR DELETE ON bills FOR EACH ROW EXECUTE FUNCTION bill_guard();

-- +goose StatementBegin
CREATE FUNCTION posted_doc_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION '% cannot be deleted; void instead', TG_TABLE_NAME USING ERRCODE = 'PXIMM';
  END IF;
  IF OLD.status = 'void' THEN
    RAISE EXCEPTION 'a voided document cannot be changed' USING ERRCODE = 'PXIMM';
  END IF;
  IF (to_jsonb(NEW) - 'status' - 'void_entry_id' - 'notes' - 'updated_at')
     <> (to_jsonb(OLD) - 'status' - 'void_entry_id' - 'notes' - 'updated_at') THEN
    RAISE EXCEPTION 'posted documents cannot be changed; void and re-enter instead' USING ERRCODE = 'PXIMM';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER supplier_payments_guard BEFORE UPDATE OR DELETE ON supplier_payments FOR EACH ROW EXECUTE FUNCTION posted_doc_guard();
CREATE TRIGGER expenses_guard BEFORE UPDATE OR DELETE ON expenses FOR EACH ROW EXECUTE FUNCTION posted_doc_guard();
CREATE TRIGGER bill_lines_immutable BEFORE UPDATE OR DELETE ON bill_lines FOR EACH ROW EXECUTE FUNCTION forbid_change();
CREATE TRIGGER supplier_allocations_immutable BEFORE UPDATE OR DELETE ON supplier_allocations FOR EACH ROW EXECUTE FUNCTION forbid_change();
CREATE TRIGGER stock_moves_immutable BEFORE UPDATE OR DELETE ON stock_moves FOR EACH ROW EXECUTE FUNCTION forbid_change();
CREATE TRIGGER stock_docs_immutable BEFORE UPDATE OR DELETE ON stock_docs FOR EACH ROW EXECUTE FUNCTION forbid_change();

CREATE TRIGGER suppliers_touch BEFORE UPDATE ON suppliers FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER items_touch BEFORE UPDATE ON items FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER bills_touch BEFORE UPDATE ON bills FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER supplier_payments_touch BEFORE UPDATE ON supplier_payments FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER expenses_touch BEFORE UPDATE ON expenses FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

CREATE TRIGGER suppliers_audit AFTER INSERT OR UPDATE OR DELETE ON suppliers FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER items_audit AFTER INSERT OR UPDATE OR DELETE ON items FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER warehouses_audit AFTER INSERT OR UPDATE OR DELETE ON warehouses FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER bills_audit AFTER INSERT OR UPDATE OR DELETE ON bills FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER supplier_payments_audit AFTER INSERT OR UPDATE OR DELETE ON supplier_payments FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER expenses_audit AFTER INSERT OR UPDATE OR DELETE ON expenses FOR EACH ROW EXECUTE FUNCTION audit_row();
CREATE TRIGGER stock_docs_audit AFTER INSERT ON stock_docs FOR EACH ROW EXECUTE FUNCTION audit_row();

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['suppliers', 'items', 'warehouses', 'stock', 'stock_moves', 'bills', 'bill_lines',
                           'supplier_payments', 'supplier_allocations', 'expenses', 'stock_docs'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
  END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS stock_docs, expenses, supplier_allocations, supplier_payments, bill_lines, bills, stock_moves, stock,
  warehouses, items, suppliers;
DROP FUNCTION IF EXISTS apply_bill_allocation(), bill_guard(), posted_doc_guard();
ALTER TABLE companies DROP COLUMN IF EXISTS ap_account_id, DROP COLUMN IF EXISTS inventory_account_id,
  DROP COLUMN IF EXISTS cogs_account_id, DROP COLUMN IF EXISTS fuel_account_id, DROP COLUMN IF EXISTS stock_adjust_account_id,
  DROP COLUMN IF EXISTS default_expense_account_id;
