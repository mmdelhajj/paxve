-- Retail core part 1: item variants (templates with attributes → one real item per variant), promotions engine,
-- loyalty points and gift cards, scale barcodes / PLU / labels, POS speed and hardware (held baskets, quick items,
-- drawer kick, customer display, denomination count), supermarket and clothing packs.
-- +goose Up

-- ---------- variants: a template item carries the attributes; each variant is a normal item (own SKU, barcode, stock) ----------
ALTER TABLE items ADD COLUMN template_id uuid,
  ADD COLUMN is_template boolean NOT NULL DEFAULT false,
  ADD COLUMN attributes jsonb NOT NULL DEFAULT '[]',     -- template: [{name, values: [..]}]
  ADD COLUMN attr_extras jsonb NOT NULL DEFAULT '{}',    -- template: {"Size=XL": "2.00"} extra price per attribute value
  ADD COLUMN variant_values jsonb NOT NULL DEFAULT '{}', -- variant: {"Size": "M", "Colour": "Red"}
  ADD COLUMN brand text NOT NULL DEFAULT '' CHECK (length(brand) <= 80),
  ADD COLUMN plu int CHECK (plu IS NULL OR plu BETWEEN 1 AND 99999),
  ADD COLUMN sold_by_weight boolean NOT NULL DEFAULT false,
  ADD COLUMN content_qty numeric(14, 4) CHECK (content_qty IS NULL OR content_qty > 0), -- for the unit price on shelf labels
  ADD COLUMN content_unit text NOT NULL DEFAULT '' CHECK (content_unit IN ('', 'kg', 'g', 'L', 'ml', 'm', 'pcs'));
ALTER TABLE items ADD CONSTRAINT items_template_same_company FOREIGN KEY (company_id, template_id) REFERENCES items (company_id, id);
CREATE INDEX items_template_idx ON items (company_id, template_id) WHERE template_id IS NOT NULL;
CREATE UNIQUE INDEX items_plu_uniq ON items (company_id, plu) WHERE plu IS NOT NULL;

-- ---------- company retail settings ----------
ALTER TABLE companies
  ADD COLUMN scale_config jsonb NOT NULL DEFAULT '{"prefixes": [{"prefix": "21", "mode": "weight", "decimals": 3}, {"prefix": "22", "mode": "price", "decimals": 2}]}',
  ADD COLUMN variant_barcode_prefix text NOT NULL DEFAULT '29' CHECK (variant_barcode_prefix ~ '^[0-9]{2,4}$'),
  ADD COLUMN gift_liability_account_id uuid,
  ADD COLUMN loyalty_liability_account_id uuid,
  ADD COLUMN loyalty_expense_account_id uuid,
  ADD COLUMN pack text NOT NULL DEFAULT '' CHECK (pack IN ('', 'supermarket', 'clothing'));
ALTER TABLE companies ADD CONSTRAINT companies_gift_liab_fk FOREIGN KEY (tenant_id, gift_liability_account_id) REFERENCES accounts (tenant_id, id);
ALTER TABLE companies ADD CONSTRAINT companies_loyalty_liab_fk FOREIGN KEY (tenant_id, loyalty_liability_account_id) REFERENCES accounts (tenant_id, id);
ALTER TABLE companies ADD CONSTRAINT companies_loyalty_exp_fk FOREIGN KEY (tenant_id, loyalty_expense_account_id) REFERENCES accounts (tenant_id, id);
ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_business_type_check;
ALTER TABLE companies ADD CONSTRAINT companies_business_type_check
  CHECK (business_type IN ('generator', 'internet', 'satellite', 'water', 'supermarket', 'clothing'));

-- gift cards and loyalty points are tendered like a cash box whose account is the liability
ALTER TABLE payment_methods DROP CONSTRAINT payment_methods_kind_check;
ALTER TABLE payment_methods ADD CONSTRAINT payment_methods_kind_check CHECK (kind IN ('cash', 'wallet', 'bank', 'card', 'cheque', 'gift', 'loyalty'));

-- ---------- promotions ----------
CREATE TABLE promotions (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  name          text NOT NULL CHECK (length(trim(name)) > 0),
  name_i18n     jsonb NOT NULL DEFAULT '{}',
  kind          text NOT NULL CHECK (kind IN ('discount', 'bxgy', 'tiers', 'bundle', 'basket')),
  applies_to    text NOT NULL DEFAULT 'all' CHECK (applies_to IN ('all', 'items', 'categories', 'brands')),
  item_ids      uuid[] NOT NULL DEFAULT '{}',
  category_ids  uuid[] NOT NULL DEFAULT '{}',
  brands        text[] NOT NULL DEFAULT '{}',
  value_type    text NOT NULL DEFAULT 'pct' CHECK (value_type IN ('pct', 'amount', 'price')),
  value         numeric(20, 4) NOT NULL DEFAULT 0 CHECK (value >= 0),
  buy_qty       numeric(14, 4) NOT NULL DEFAULT 0 CHECK (buy_qty >= 0),
  get_qty       numeric(14, 4) NOT NULL DEFAULT 0 CHECK (get_qty >= 0),
  tiers         jsonb NOT NULL DEFAULT '[]',  -- [{min_qty, pct}]
  bundle        jsonb NOT NULL DEFAULT '[]',  -- [{item_id, qty}]
  min_amount    numeric(20, 4) NOT NULL DEFAULT 0 CHECK (min_amount >= 0),
  currency      char(3) REFERENCES currencies (code), -- amounts / prices / thresholds are in this currency (converted for other tills)
  coupon_code   text NOT NULL DEFAULT '' CHECK (coupon_code = '' OR coupon_code ~ '^[A-Z0-9_-]{3,32}$'),
  date_from     date,
  date_to       date,
  days          int[] NOT NULL DEFAULT '{}',  -- 0 = Sunday … 6 = Saturday; empty = every day
  time_from     time,
  time_to       time,
  register_ids  uuid[] NOT NULL DEFAULT '{}',
  price_list_ids uuid[] NOT NULL DEFAULT '{}',
  stackable     boolean NOT NULL DEFAULT false,
  priority      int NOT NULL DEFAULT 0,
  max_uses      int CHECK (max_uses IS NULL OR max_uses > 0),
  uses          int NOT NULL DEFAULT 0,
  is_active     boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id),
  CHECK (date_to IS NULL OR date_from IS NULL OR date_to >= date_from)
);
CREATE INDEX promotions_active_idx ON promotions (company_id) WHERE is_active;
CREATE UNIQUE INDEX promotions_coupon_idx ON promotions (company_id, coupon_code) WHERE coupon_code <> '';
CREATE TABLE promotion_uses (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  promotion_id uuid NOT NULL,
  source       text NOT NULL CHECK (source IN ('pos', 'invoice')),
  source_id    uuid NOT NULL,
  currency     char(3) NOT NULL REFERENCES currencies (code),
  discount     numeric(20, 4) NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT promotion_uses_promo_same_company FOREIGN KEY (company_id, promotion_id) REFERENCES promotions (company_id, id)
);
CREATE INDEX promotion_uses_promo_idx ON promotion_uses (company_id, promotion_id, created_at);

-- ---------- loyalty ----------
CREATE TABLE loyalty_programs (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  is_active     boolean NOT NULL DEFAULT true,
  earn_per      numeric(14, 4) NOT NULL DEFAULT 1 CHECK (earn_per >= 0),       -- points per 1 unit of base currency spent
  point_value   numeric(14, 6) NOT NULL DEFAULT 0.01 CHECK (point_value >= 0), -- base currency per point when redeemed
  min_redeem    numeric(14, 2) NOT NULL DEFAULT 0 CHECK (min_redeem >= 0),
  expiry_months int NOT NULL DEFAULT 0 CHECK (expiry_months BETWEEN 0 AND 120),
  tiers         jsonb NOT NULL DEFAULT '[]', -- [{name, min_points, multiplier}] on lifetime points
  updated_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id),
  UNIQUE (company_id, id)
);
ALTER TABLE customers ADD COLUMN loyalty_card text NOT NULL DEFAULT '' CHECK (length(loyalty_card) <= 40),
  ADD COLUMN loyalty_points numeric(16, 2) NOT NULL DEFAULT 0,
  ADD COLUMN loyalty_lifetime numeric(16, 2) NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX customers_loyalty_card_uniq ON customers (company_id, loyalty_card) WHERE loyalty_card <> '';
CREATE TABLE loyalty_moves (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  customer_id      uuid NOT NULL,
  kind             text NOT NULL CHECK (kind IN ('earn', 'redeem', 'adjust', 'expire', 'reverse')),
  points           numeric(16, 2) NOT NULL,
  value            numeric(20, 4) NOT NULL DEFAULT 0, -- base currency
  source           text NOT NULL DEFAULT '',
  source_id        uuid,
  journal_entry_id uuid,
  note             text NOT NULL DEFAULT '',
  created_by       uuid DEFAULT app_user(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, customer_id) REFERENCES customers (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX loyalty_moves_cust_idx ON loyalty_moves (company_id, customer_id, created_at DESC);

-- ---------- gift cards / vouchers ----------
CREATE TABLE gift_cards (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  code        text NOT NULL CHECK (code ~ '^[A-Z0-9-]{4,32}$'),
  currency    char(3) NOT NULL REFERENCES currencies (code),
  initial     numeric(20, 4) NOT NULL CHECK (initial > 0),
  balance     numeric(20, 4) NOT NULL CHECK (balance >= 0),
  customer_id uuid,
  status      text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'used', 'void')),
  expires_on  date,
  source      text NOT NULL DEFAULT 'pos' CHECK (source IN ('pos', 'manual', 'promo')),
  source_id   uuid,
  journal_entry_id uuid,
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  FOREIGN KEY (tenant_id, customer_id) REFERENCES customers (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entries (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, code)
);
CREATE TABLE gift_card_moves (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  card_id    uuid NOT NULL,
  kind       text NOT NULL CHECK (kind IN ('issue', 'redeem', 'void', 'refund')),
  amount     numeric(20, 4) NOT NULL,
  source     text NOT NULL DEFAULT '',
  source_id  uuid,
  created_by uuid DEFAULT app_user(),
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT gift_card_moves_card_same_company FOREIGN KEY (company_id, card_id) REFERENCES gift_cards (company_id, id)
);
CREATE INDEX gift_card_moves_card_idx ON gift_card_moves (company_id, card_id, created_at);

-- ---------- POS speed and hardware ----------
ALTER TABLE pos_registers ADD COLUMN cashier_quick_item boolean NOT NULL DEFAULT false,
  ADD COLUMN drawer_kick boolean NOT NULL DEFAULT false,
  ADD COLUMN customer_display boolean NOT NULL DEFAULT false;
ALTER TABLE pos_sessions ADD COLUMN counted_detail jsonb NOT NULL DEFAULT '{}'; -- {method_id: {"100": 3, "50": 1}} denominations counted
ALTER TABLE pos_orders ADD COLUMN promos jsonb NOT NULL DEFAULT '[]', -- [{id, name, discount}]
  ADD COLUMN loyalty jsonb NOT NULL DEFAULT '{}',                     -- {earned, redeemed, balance}
  ADD COLUMN gift jsonb NOT NULL DEFAULT '[]';                        -- [{code, amount, kind: sold|redeemed}]
CREATE TABLE pos_held_baskets (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  register_id uuid NOT NULL,
  label       text NOT NULL DEFAULT '' CHECK (length(label) <= 80),
  payload     jsonb NOT NULL CHECK (length(payload::text) <= 200000),
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT pos_held_baskets_register_same_company FOREIGN KEY (company_id, register_id) REFERENCES pos_registers (company_id, id),
  UNIQUE (company_id, id)
);

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['promotions', 'promotion_uses', 'loyalty_programs', 'loyalty_moves', 'gift_cards', 'gift_card_moves', 'pos_held_baskets'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
    IF t <> 'pos_held_baskets' THEN
      EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    END IF;
  END LOOP;
END
$$;
-- +goose StatementEnd
GRANT DELETE ON pos_held_baskets TO paxve_app;
CREATE TRIGGER loyalty_moves_immutable BEFORE UPDATE OR DELETE ON loyalty_moves FOR EACH ROW EXECUTE FUNCTION forbid_change();
CREATE TRIGGER gift_card_moves_immutable BEFORE UPDATE OR DELETE ON gift_card_moves FOR EACH ROW EXECUTE FUNCTION forbid_change();
CREATE TRIGGER promotion_uses_immutable BEFORE UPDATE OR DELETE ON promotion_uses FOR EACH ROW EXECUTE FUNCTION forbid_change();

-- +goose Down
SELECT 1;
