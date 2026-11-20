-- Phase 1, warehouse pro: locations and bins (zone → aisle → shelf → bin) with stock per bin and lot, putaway rules,
-- removal strategies (FIFO / FEFO / closest bin), multi-step receipts (receive → QC → stock) and deliveries
-- (pick → pack → ship) as warehouse operations, packages and pallets, batch / wave picking, dropship (a sales
-- order makes a purchase order shipped straight to the customer), cross-dock, delivery trips with drivers,
-- vehicles and proof of delivery.
--
-- Money never moves here: the value of stock stays per item and warehouse (stock, stock_moves, the journal).
-- Bins are the PHYSICAL place of what the warehouse holds. Stock that no bin holds is "unbinned" (on the floor /
-- main stock), so a warehouse without bins works exactly as before, and every existing flow (bills, sales, POS,
-- transfers, counts…) keeps working: when stock leaves a warehouse, a trigger takes it out of the unbinned part
-- first, then from the bins (output / pack areas first, then the removal strategy).

-- +goose Up
ALTER TABLE warehouses ADD COLUMN code text NOT NULL DEFAULT '';
ALTER TABLE warehouses ADD COLUMN use_bins boolean NOT NULL DEFAULT false;
ALTER TABLE warehouses ADD COLUMN reception_steps smallint NOT NULL DEFAULT 1 CHECK (reception_steps BETWEEN 1 AND 3);
ALTER TABLE warehouses ADD COLUMN delivery_steps smallint NOT NULL DEFAULT 1 CHECK (delivery_steps BETWEEN 1 AND 3);
ALTER TABLE warehouses ADD COLUMN removal text NOT NULL DEFAULT 'fifo' CHECK (removal IN ('fifo', 'fefo', 'closest'));
ALTER TABLE warehouses ADD COLUMN crossdock boolean NOT NULL DEFAULT false;
ALTER TABLE warehouses ADD COLUMN is_transit boolean NOT NULL DEFAULT false;
ALTER TABLE warehouses ADD COLUMN address text NOT NULL DEFAULT '';

-- same-company references need (company_id, id) keys on the referenced tables
CREATE UNIQUE INDEX IF NOT EXISTS sales_orders_company_id_key ON sales_orders (company_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS sales_deliveries_company_id_key ON sales_deliveries (company_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS item_categories_company_id_key ON item_categories (company_id, id);

-- ---- locations ----
CREATE TABLE wh_locations (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  parent_id    uuid,
  kind         text NOT NULL CHECK (kind IN ('zone', 'aisle', 'shelf', 'bin', 'input', 'qc', 'output', 'pack')),
  code         text NOT NULL CHECK (length(trim(code)) BETWEEN 1 AND 60),
  name         text NOT NULL DEFAULT '',
  barcode      text NOT NULL DEFAULT '',
  sort_key     text NOT NULL DEFAULT '',
  is_active    boolean NOT NULL DEFAULT true,
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT wh_locations_warehouse_id_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (warehouse_id, code)
);
ALTER TABLE wh_locations ADD CONSTRAINT wh_locations_parent_id_same_company FOREIGN KEY (company_id, parent_id) REFERENCES wh_locations (company_id, id);
CREATE UNIQUE INDEX wh_locations_barcode_idx ON wh_locations (company_id, barcode) WHERE barcode <> '';
CREATE INDEX wh_locations_wh_idx ON wh_locations (warehouse_id, sort_key);

-- what each bin holds (physical); lot_id for lot / serial tracked items
CREATE TABLE wh_bin_stock (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  location_id  uuid NOT NULL,
  item_id      uuid NOT NULL,
  lot_id       uuid,
  quantity     numeric(18, 4) NOT NULL CHECK (quantity >= 0),
  in_date      date NOT NULL DEFAULT current_date,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT wh_bin_stock_warehouse_id_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id),
  CONSTRAINT wh_bin_stock_location_id_same_company FOREIGN KEY (company_id, location_id) REFERENCES wh_locations (company_id, id),
  CONSTRAINT wh_bin_stock_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  CONSTRAINT wh_bin_stock_lot_id_same_company FOREIGN KEY (company_id, lot_id) REFERENCES stock_lots (company_id, id)
);
CREATE UNIQUE INDEX wh_bin_stock_key ON wh_bin_stock (location_id, item_id, (coalesce(lot_id, '00000000-0000-0000-0000-000000000000'::uuid)));
CREATE INDEX wh_bin_stock_item_idx ON wh_bin_stock (warehouse_id, item_id);

-- ---- putaway rules: where an item (or a category) goes when it arrives ----
CREATE TABLE wh_putaway_rules (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  item_id      uuid,
  category_id  uuid,
  location_id  uuid NOT NULL,
  priority     int NOT NULL DEFAULT 10,
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT wh_putaway_rules_warehouse_id_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id),
  CONSTRAINT wh_putaway_rules_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  CONSTRAINT wh_putaway_rules_category_id_same_company FOREIGN KEY (company_id, category_id) REFERENCES item_categories (company_id, id),
  CONSTRAINT wh_putaway_rules_location_id_same_company FOREIGN KEY (company_id, location_id) REFERENCES wh_locations (company_id, id),
  UNIQUE (company_id, id),
  CHECK (item_id IS NOT NULL OR category_id IS NOT NULL)
);

-- ---- batch / wave picking ----
CREATE TABLE wh_batches (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  number       bigint NOT NULL,
  warehouse_id uuid NOT NULL,
  kind         text NOT NULL DEFAULT 'batch' CHECK (kind IN ('batch', 'wave')),
  status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done', 'cancelled')),
  assigned_to  uuid REFERENCES users (id),
  notes        text NOT NULL DEFAULT '',
  created_by   uuid DEFAULT app_user(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  done_at      timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT wh_batches_warehouse_id_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);

-- ---- warehouse operations (transfer documents per step) ----
CREATE TABLE wh_ops (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id        uuid NOT NULL,
  number            bigint NOT NULL,
  warehouse_id      uuid NOT NULL,
  kind              text NOT NULL CHECK (kind IN ('putaway', 'qc', 'pick', 'pack', 'ship', 'internal')),
  status            text NOT NULL DEFAULT 'ready' CHECK (status IN ('waiting', 'ready', 'done', 'cancelled')),
  origin            text NOT NULL DEFAULT '',
  purchase_order_id uuid,
  receipt_id        uuid,
  sales_order_id    uuid,
  batch_id          uuid,
  prev_op_id        uuid,
  delivery_id       uuid,
  scheduled_date    date NOT NULL DEFAULT current_date,
  notes             text NOT NULL DEFAULT '',
  done_at           timestamptz,
  done_by           uuid REFERENCES users (id),
  created_by        uuid DEFAULT app_user(),
  created_at        timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT wh_ops_warehouse_id_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id),
  CONSTRAINT wh_ops_purchase_order_id_same_company FOREIGN KEY (company_id, purchase_order_id) REFERENCES purchase_orders (company_id, id),
  CONSTRAINT wh_ops_receipt_id_same_company FOREIGN KEY (company_id, receipt_id) REFERENCES goods_receipts (company_id, id),
  CONSTRAINT wh_ops_sales_order_id_same_company FOREIGN KEY (company_id, sales_order_id) REFERENCES sales_orders (company_id, id),
  CONSTRAINT wh_ops_batch_id_same_company FOREIGN KEY (company_id, batch_id) REFERENCES wh_batches (company_id, id),
  CONSTRAINT wh_ops_delivery_id_same_company FOREIGN KEY (company_id, delivery_id) REFERENCES sales_deliveries (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
ALTER TABLE wh_ops ADD CONSTRAINT wh_ops_prev_op_id_same_company FOREIGN KEY (company_id, prev_op_id) REFERENCES wh_ops (company_id, id);
CREATE INDEX wh_ops_list_idx ON wh_ops (company_id, status, kind, scheduled_date);
CREATE INDEX wh_ops_order_idx ON wh_ops (sales_order_id) WHERE sales_order_id IS NOT NULL;
CREATE INDEX wh_ops_receipt_idx ON wh_ops (receipt_id) WHERE receipt_id IS NOT NULL;

-- ---- packages and pallets ----
CREATE TABLE wh_packages (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id     uuid NOT NULL,
  number         bigint NOT NULL,
  warehouse_id   uuid NOT NULL,
  kind           text NOT NULL DEFAULT 'box' CHECK (kind IN ('box', 'pallet')),
  parent_id      uuid,
  sales_order_id uuid,
  delivery_id    uuid,
  status         text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'sealed', 'shipped', 'cancelled')),
  weight         numeric(12, 3) NOT NULL DEFAULT 0 CHECK (weight >= 0),
  length_cm      numeric(10, 2) NOT NULL DEFAULT 0 CHECK (length_cm >= 0),
  width_cm       numeric(10, 2) NOT NULL DEFAULT 0 CHECK (width_cm >= 0),
  height_cm      numeric(10, 2) NOT NULL DEFAULT 0 CHECK (height_cm >= 0),
  notes          text NOT NULL DEFAULT '',
  created_by     uuid DEFAULT app_user(),
  created_at     timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT wh_packages_warehouse_id_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id),
  CONSTRAINT wh_packages_sales_order_id_same_company FOREIGN KEY (company_id, sales_order_id) REFERENCES sales_orders (company_id, id),
  CONSTRAINT wh_packages_delivery_id_same_company FOREIGN KEY (company_id, delivery_id) REFERENCES sales_deliveries (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
ALTER TABLE wh_packages ADD CONSTRAINT wh_packages_parent_id_same_company FOREIGN KEY (company_id, parent_id) REFERENCES wh_packages (company_id, id);

CREATE TABLE wh_package_lines (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  package_id uuid NOT NULL,
  item_id    uuid NOT NULL,
  lot_id     uuid,
  quantity   numeric(18, 4) NOT NULL CHECK (quantity > 0),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT wh_package_lines_package_id_same_company FOREIGN KEY (company_id, package_id) REFERENCES wh_packages (company_id, id),
  CONSTRAINT wh_package_lines_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  CONSTRAINT wh_package_lines_lot_id_same_company FOREIGN KEY (company_id, lot_id) REFERENCES stock_lots (company_id, id)
);
CREATE INDEX wh_package_lines_pkg_idx ON wh_package_lines (package_id);

CREATE TABLE wh_op_lines (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  op_id            uuid NOT NULL,
  line_no          int NOT NULL,
  item_id          uuid NOT NULL,
  lot_id           uuid,
  so_line_id       uuid,
  quantity         numeric(18, 4) NOT NULL CHECK (quantity > 0),
  done_qty         numeric(18, 4) NOT NULL DEFAULT 0 CHECK (done_qty >= 0),
  from_location_id uuid,
  to_location_id   uuid,
  package_id       uuid,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT wh_op_lines_op_id_same_company FOREIGN KEY (company_id, op_id) REFERENCES wh_ops (company_id, id),
  CONSTRAINT wh_op_lines_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  CONSTRAINT wh_op_lines_lot_id_same_company FOREIGN KEY (company_id, lot_id) REFERENCES stock_lots (company_id, id),
  CONSTRAINT wh_op_lines_from_location_id_same_company FOREIGN KEY (company_id, from_location_id) REFERENCES wh_locations (company_id, id),
  CONSTRAINT wh_op_lines_to_location_id_same_company FOREIGN KEY (company_id, to_location_id) REFERENCES wh_locations (company_id, id),
  CONSTRAINT wh_op_lines_package_id_same_company FOREIGN KEY (company_id, package_id) REFERENCES wh_packages (company_id, id),
  FOREIGN KEY (tenant_id, so_line_id) REFERENCES sales_order_lines (tenant_id, id),
  UNIQUE (op_id, line_no)
);

-- ---- dropship ----
ALTER TABLE purchase_orders ADD COLUMN dropship_order_id uuid;
ALTER TABLE purchase_orders ADD CONSTRAINT purchase_orders_dropship_order_id_same_company FOREIGN KEY (company_id, dropship_order_id) REFERENCES sales_orders (company_id, id);
ALTER TABLE purchase_orders ADD COLUMN ship_to text NOT NULL DEFAULT '';
ALTER TABLE purchase_order_lines ADD COLUMN so_line_id uuid;
ALTER TABLE purchase_order_lines ADD CONSTRAINT purchase_order_lines_so_line_fk FOREIGN KEY (tenant_id, so_line_id) REFERENCES sales_order_lines (tenant_id, id);

-- ---- vehicles, delivery trips and proof of delivery ----
CREATE TABLE vehicles (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  name        text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
  plate       text NOT NULL DEFAULT '',
  capacity_kg numeric(12, 2) NOT NULL DEFAULT 0 CHECK (capacity_kg >= 0),
  driver_id   uuid REFERENCES users (id),
  is_active   boolean NOT NULL DEFAULT true,
  notes       text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, name)
);

CREATE TABLE delivery_trips (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  number      bigint NOT NULL,
  trip_date   date NOT NULL,
  vehicle_id  uuid,
  driver_id   uuid REFERENCES users (id),
  status      text NOT NULL DEFAULT 'planned' CHECK (status IN ('planned', 'out', 'done', 'cancelled')),
  notes       text NOT NULL DEFAULT '',
  started_at  timestamptz,
  finished_at timestamptz,
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT delivery_trips_vehicle_id_same_company FOREIGN KEY (company_id, vehicle_id) REFERENCES vehicles (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
CREATE INDEX delivery_trips_date_idx ON delivery_trips (company_id, trip_date);

CREATE TABLE trip_stops (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id        uuid NOT NULL,
  trip_id           uuid NOT NULL,
  seq               int NOT NULL,
  sales_delivery_id uuid,
  sales_order_id    uuid,
  customer_id       uuid,
  address           text NOT NULL DEFAULT '',
  phone             text NOT NULL DEFAULT '',
  note              text NOT NULL DEFAULT '',
  status            text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'failed')),
  pod_name          text NOT NULL DEFAULT '',
  pod_signature     text NOT NULL DEFAULT '' CHECK (length(pod_signature) <= 400000),
  pod_note          text NOT NULL DEFAULT '',
  lat               numeric(9, 6),
  lng               numeric(9, 6),
  delivered_at      timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT trip_stops_trip_id_same_company FOREIGN KEY (company_id, trip_id) REFERENCES delivery_trips (company_id, id),
  CONSTRAINT trip_stops_sales_delivery_id_same_company FOREIGN KEY (company_id, sales_delivery_id) REFERENCES sales_deliveries (company_id, id),
  CONSTRAINT trip_stops_sales_order_id_same_company FOREIGN KEY (company_id, sales_order_id) REFERENCES sales_orders (company_id, id),
  CONSTRAINT trip_stops_customer_id_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX trip_stops_trip_idx ON trip_stops (trip_id, seq);
CREATE INDEX trip_stops_delivery_idx ON trip_stops (sales_delivery_id) WHERE sales_delivery_id IS NOT NULL;

-- attachments: the document types are listed by the application (attachTables); the database only checks the shape
-- (the old fixed list missed subscriptions, repairs, instalments and leads, so files on them were refused)
ALTER TABLE attachments DROP CONSTRAINT IF EXISTS attachments_entity_type_check;
ALTER TABLE attachments ADD CONSTRAINT attachments_entity_type_check CHECK (entity_type ~ '^[a-z_]{2,40}$');

-- ---- stock leaving a warehouse leaves its bins too ----
-- +goose StatementBegin
CREATE FUNCTION wh_bins_on_stock_out() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_binned  numeric;
  v_take    numeric;
  v_removal text;
  v_q       numeric;
  r         record;
BEGIN
  SELECT coalesce(sum(quantity), 0) INTO v_binned FROM wh_bin_stock WHERE warehouse_id = NEW.warehouse_id AND item_id = NEW.item_id;
  IF v_binned <= 0 THEN
    RETURN NEW;
  END IF;
  -- what left the warehouse comes out of the unbinned part first
  v_take := (OLD.quantity - NEW.quantity) - greatest(OLD.quantity - v_binned, 0);
  IF v_take <= 0 THEN
    RETURN NEW;
  END IF;
  SELECT removal INTO v_removal FROM warehouses WHERE id = NEW.warehouse_id;
  FOR r IN
    SELECT b.id, b.quantity FROM wh_bin_stock b JOIN wh_locations l ON l.id = b.location_id LEFT JOIN stock_lots sl ON sl.id = b.lot_id
    WHERE b.warehouse_id = NEW.warehouse_id AND b.item_id = NEW.item_id AND b.quantity > 0
    ORDER BY CASE l.kind WHEN 'output' THEN 0 WHEN 'pack' THEN 1 WHEN 'qc' THEN 3 WHEN 'input' THEN 4 ELSE 2 END,
             CASE WHEN v_removal = 'fefo' THEN sl.expiry END NULLS LAST,
             CASE WHEN v_removal = 'closest' THEN l.sort_key END,
             b.in_date, b.id
    FOR UPDATE OF b
  LOOP
    EXIT WHEN v_take <= 0;
    v_q := least(r.quantity, v_take);
    UPDATE wh_bin_stock SET quantity = quantity - v_q, updated_at = now() WHERE id = r.id;
    v_take := v_take - v_q;
  END LOOP;
  DELETE FROM wh_bin_stock WHERE warehouse_id = NEW.warehouse_id AND item_id = NEW.item_id AND quantity = 0;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER stock_wh_bins AFTER UPDATE OF quantity ON stock FOR EACH ROW WHEN (NEW.quantity < OLD.quantity) EXECUTE FUNCTION wh_bins_on_stock_out();

-- row security, grants and the audit trail
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['wh_locations', 'wh_bin_stock', 'wh_putaway_rules', 'wh_batches', 'wh_ops', 'wh_op_lines', 'wh_packages',
                           'wh_package_lines', 'vehicles', 'delivery_trips', 'trip_stops'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
    IF t NOT IN ('wh_bin_stock', 'wh_op_lines', 'wh_package_lines') THEN
      EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    END IF;
  END LOOP;
END
$$;
-- +goose StatementEnd
GRANT DELETE ON wh_bin_stock, wh_putaway_rules, wh_package_lines, wh_op_lines, trip_stops TO paxve_app;

-- +goose Down
DROP TRIGGER IF EXISTS stock_wh_bins ON stock;
DROP FUNCTION IF EXISTS wh_bins_on_stock_out();
DROP TABLE IF EXISTS trip_stops, delivery_trips, vehicles, wh_op_lines, wh_package_lines, wh_packages, wh_ops, wh_batches,
  wh_putaway_rules, wh_bin_stock, wh_locations;
ALTER TABLE purchase_order_lines DROP COLUMN IF EXISTS so_line_id;
ALTER TABLE purchase_orders DROP COLUMN IF EXISTS ship_to;
ALTER TABLE purchase_orders DROP COLUMN IF EXISTS dropship_order_id;
ALTER TABLE warehouses DROP COLUMN IF EXISTS address, DROP COLUMN IF EXISTS is_transit, DROP COLUMN IF EXISTS crossdock,
  DROP COLUMN IF EXISTS removal, DROP COLUMN IF EXISTS delivery_steps, DROP COLUMN IF EXISTS reception_steps,
  DROP COLUMN IF EXISTS use_bins, DROP COLUMN IF EXISTS code;
