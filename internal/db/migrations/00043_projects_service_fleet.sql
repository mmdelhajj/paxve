-- Phase 3: projects (stages, kanban tasks with subtasks / checklist, milestones, timesheets with a timer, billing by
-- time, milestone or fixed price, budget and profitability), field service (jobs from a ticket or a customer, a
-- dispatch board, the technician's phone view with parts from stock, signature, photo and location, invoicing),
-- helpdesk SLA (policies, response / resolution targets, breaches, escalation, inbound e-mail / WhatsApp tickets,
-- customer rating), no-code automation rules (event or schedule → conditions → actions, run log, loop guard) and
-- fleet (vehicles extend the delivery vehicles: fuel with consumption checks, service plans by km / date, document
-- expiry, costs posted as expenses).
--
-- Money: project invoices are normal invoices; timesheet cost is hours × the employee's hourly cost (base currency);
-- linked expenses / bills count at their journal amounts. Fleet costs are normal expenses linked to the vehicle.

-- +goose Up

-- ---------- apps: projects (prj) and fleet (flt): on for new companies, off for existing ones until switched on ----------
ALTER TABLE companies DROP CONSTRAINT companies_apps_known;
ALTER TABLE companies ADD CONSTRAINT companies_apps_known
  CHECK (apps <@ ARRAY['gen','acc','col','msg','stk','pur','net','sat','pos','hr','crm','svc','mfg','prj','flt']::text[]);
ALTER TABLE companies ALTER COLUMN apps SET DEFAULT '{gen,acc,col,msg,stk,pur,net,sat,pos,hr,crm,svc,mfg,prj,flt}';

ALTER TABLE user_notifications DROP CONSTRAINT IF EXISTS user_notifications_kind_check;
ALTER TABLE user_notifications ADD CONSTRAINT user_notifications_kind_check
  CHECK (kind IN ('mention', 'activity', 'activity_due', 'repair', 'system', 'task', 'service', 'sla', 'automation', 'fleet'));

-- hourly cost of an employee (base currency) for timesheets; 0 = salary / 208 hours when paid in the base currency
ALTER TABLE employees ADD COLUMN hourly_cost numeric(20, 4) NOT NULL DEFAULT 0 CHECK (hourly_cost >= 0);

-- ======================= projects =======================
CREATE TABLE projects (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  number       bigint NOT NULL,
  code         text NOT NULL DEFAULT '' CHECK (length(code) <= 30),
  name         text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 200),
  customer_id  uuid,
  manager_id   uuid REFERENCES users (id),
  billing      text NOT NULL DEFAULT 'tm' CHECK (billing IN ('none', 'tm', 'fixed', 'milestone')),
  currency     char(3) NOT NULL REFERENCES currencies (code),
  hourly_rate  numeric(20, 4) NOT NULL DEFAULT 0 CHECK (hourly_rate >= 0),
  fixed_price  numeric(20, 4) NOT NULL DEFAULT 0 CHECK (fixed_price >= 0),
  budget       numeric(20, 4) NOT NULL DEFAULT 0 CHECK (budget >= 0),   -- cost budget, base currency
  budget_hours numeric(12, 2) NOT NULL DEFAULT 0 CHECK (budget_hours >= 0),
  start_date   date,
  end_date     date,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'on_hold', 'done', 'cancelled')),
  description  text NOT NULL DEFAULT '' CHECK (length(description) <= 8000),
  color        text NOT NULL DEFAULT '' CHECK (length(color) <= 20),
  created_by   uuid DEFAULT app_user(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT projects_customer_id_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number),
  CHECK (end_date IS NULL OR start_date IS NULL OR end_date >= start_date),
  CHECK (billing = 'none' OR customer_id IS NOT NULL)
);

CREATE TABLE project_stages (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  project_id uuid NOT NULL,
  seq        int NOT NULL,
  name       text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 80),
  is_done    boolean NOT NULL DEFAULT false,   -- tasks in this column count as done
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT project_stages_project_id_same_company FOREIGN KEY (company_id, project_id) REFERENCES projects (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX project_stages_project_idx ON project_stages (project_id, seq);

CREATE TABLE project_milestones (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  project_id  uuid NOT NULL,
  name        text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 200),
  due_date    date,
  amount      numeric(20, 4) NOT NULL DEFAULT 0 CHECK (amount >= 0),  -- billed when reached (milestone billing)
  reached_at  timestamptz,
  invoice_id  uuid,
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT project_milestones_project_id_same_company FOREIGN KEY (company_id, project_id) REFERENCES projects (company_id, id),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoices (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX project_milestones_project_idx ON project_milestones (project_id, due_date);

CREATE TABLE project_tasks (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  project_id    uuid NOT NULL,
  number        bigint NOT NULL,
  stage_id      uuid,
  parent_id     uuid,
  milestone_id  uuid,
  ticket_id     uuid,
  title         text NOT NULL CHECK (length(trim(title)) BETWEEN 1 AND 300),
  description   text NOT NULL DEFAULT '' CHECK (length(description) <= 8000),
  assignee_id   uuid REFERENCES users (id),
  priority      text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  deadline      date,
  planned_hours numeric(10, 2) NOT NULL DEFAULT 0 CHECK (planned_hours >= 0),
  checklist     jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(checklist) = 'array' AND jsonb_array_length(checklist) <= 100),
  tags          text[] NOT NULL DEFAULT '{}',
  seq           int NOT NULL DEFAULT 0,
  done_at       timestamptz,
  created_by    uuid DEFAULT app_user(),
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT project_tasks_project_id_same_company FOREIGN KEY (company_id, project_id) REFERENCES projects (company_id, id),
  CONSTRAINT project_tasks_stage_id_same_company FOREIGN KEY (company_id, stage_id) REFERENCES project_stages (company_id, id),
  CONSTRAINT project_tasks_parent_id_same_company FOREIGN KEY (company_id, parent_id) REFERENCES project_tasks (company_id, id),
  CONSTRAINT project_tasks_milestone_id_same_company FOREIGN KEY (company_id, milestone_id) REFERENCES project_milestones (company_id, id),
  CONSTRAINT project_tasks_ticket_id_same_company FOREIGN KEY (company_id, ticket_id) REFERENCES tickets (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number),
  CHECK (parent_id IS NULL OR parent_id <> id)
);
CREATE INDEX project_tasks_project_idx ON project_tasks (project_id, stage_id, seq);
CREATE INDEX project_tasks_assignee_idx ON project_tasks (company_id, assignee_id) WHERE done_at IS NULL;

CREATE TABLE timesheets (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id    uuid NOT NULL,
  project_id    uuid NOT NULL,
  task_id       uuid,
  employee_id   uuid NOT NULL,
  user_id       uuid REFERENCES users (id),
  work_date     date NOT NULL,
  hours         numeric(8, 2) NOT NULL DEFAULT 0 CHECK (hours >= 0 AND hours <= 24),
  description   text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
  billable      boolean NOT NULL DEFAULT true,
  hourly_cost   numeric(20, 4) NOT NULL DEFAULT 0,      -- base currency, frozen when written
  cost          numeric(20, 4) NOT NULL DEFAULT 0,
  running_since timestamptz,                             -- a running timer (hours added when stopped)
  invoice_id    uuid,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT timesheets_project_id_same_company FOREIGN KEY (company_id, project_id) REFERENCES projects (company_id, id),
  CONSTRAINT timesheets_task_id_same_company FOREIGN KEY (company_id, task_id) REFERENCES project_tasks (company_id, id),
  FOREIGN KEY (tenant_id, employee_id) REFERENCES employees (tenant_id, id),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoices (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX timesheets_project_idx ON timesheets (project_id, work_date);
CREATE INDEX timesheets_employee_idx ON timesheets (company_id, employee_id, work_date);
CREATE UNIQUE INDEX timesheets_one_timer ON timesheets (employee_id) WHERE running_since IS NOT NULL;

-- documents counted on a project (invoices issued from it, expenses and supplier bills linked to it)
CREATE TABLE project_links (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  project_id uuid NOT NULL,
  doc_type   text NOT NULL CHECK (doc_type IN ('invoice', 'expense', 'bill')),
  doc_id     uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT project_links_project_id_same_company FOREIGN KEY (company_id, project_id) REFERENCES projects (company_id, id),
  UNIQUE (company_id, id),
  UNIQUE (doc_type, doc_id)
);
CREATE INDEX project_links_project_idx ON project_links (project_id, doc_type);

-- ======================= field service =======================
CREATE TABLE service_jobs (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id      uuid NOT NULL,
  number          bigint NOT NULL,
  customer_id     uuid NOT NULL,
  ticket_id       uuid,
  project_id      uuid,
  title           text NOT NULL CHECK (length(trim(title)) BETWEEN 1 AND 300),
  description     text NOT NULL DEFAULT '' CHECK (length(description) <= 8000),
  address         text NOT NULL DEFAULT '' CHECK (length(address) <= 500),
  phone           text NOT NULL DEFAULT '' CHECK (length(phone) <= 40),
  technician_id   uuid REFERENCES users (id),
  scheduled_start timestamptz,
  duration_min    int NOT NULL DEFAULT 60 CHECK (duration_min BETWEEN 5 AND 10080),
  priority        text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  status          text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'scheduled', 'in_progress', 'done', 'cancelled')),
  warehouse_id    uuid,                                  -- where the parts come from (the van)
  currency        char(3) NOT NULL REFERENCES currencies (code),
  labour_rate     numeric(20, 4) NOT NULL DEFAULT 0 CHECK (labour_rate >= 0),
  labour_hours    numeric(8, 2) CHECK (labour_hours IS NULL OR (labour_hours >= 0 AND labour_hours <= 200)),
  started_at      timestamptz,
  finished_at     timestamptz,
  report          text NOT NULL DEFAULT '' CHECK (length(report) <= 8000),
  signed_by       text NOT NULL DEFAULT '' CHECK (length(signed_by) <= 120),
  signature       text NOT NULL DEFAULT '' CHECK (length(signature) <= 400000),
  lat             numeric(9, 6),
  lng             numeric(9, 6),
  invoice_id      uuid,
  created_by      uuid DEFAULT app_user(),
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT service_jobs_customer_id_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  CONSTRAINT service_jobs_ticket_id_same_company FOREIGN KEY (company_id, ticket_id) REFERENCES tickets (company_id, id),
  CONSTRAINT service_jobs_project_id_same_company FOREIGN KEY (company_id, project_id) REFERENCES projects (company_id, id),
  CONSTRAINT service_jobs_warehouse_id_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoices (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
CREATE INDEX service_jobs_board_idx ON service_jobs (company_id, scheduled_start);
CREATE INDEX service_jobs_tech_idx ON service_jobs (company_id, technician_id, status);

CREATE TABLE service_job_parts (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  job_id       uuid NOT NULL,
  line_no      int NOT NULL,
  item_id      uuid NOT NULL,
  warehouse_id uuid,
  description  text NOT NULL DEFAULT '' CHECK (length(description) <= 300),
  quantity     numeric(18, 4) NOT NULL CHECK (quantity > 0),
  unit_price   numeric(20, 4) NOT NULL DEFAULT 0 CHECK (unit_price >= 0),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT service_job_parts_job_id_same_company FOREIGN KEY (company_id, job_id) REFERENCES service_jobs (company_id, id),
  CONSTRAINT service_job_parts_item_id_same_company FOREIGN KEY (company_id, item_id) REFERENCES items (company_id, id),
  CONSTRAINT service_job_parts_warehouse_id_same_company FOREIGN KEY (company_id, warehouse_id) REFERENCES warehouses (company_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX service_job_parts_job_idx ON service_job_parts (job_id, line_no);

-- ======================= helpdesk SLA =======================
CREATE TABLE sla_policies (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id       uuid NOT NULL,
  name             text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
  priority         text CHECK (priority IS NULL OR priority IN ('low', 'normal', 'high', 'urgent')),
  category         text CHECK (category IS NULL OR length(category) <= 40),
  response_hours   numeric(8, 2) NOT NULL CHECK (response_hours > 0),
  resolution_hours numeric(8, 2) NOT NULL CHECK (resolution_hours > 0),
  escalate_to      uuid REFERENCES users (id),
  is_active        boolean NOT NULL DEFAULT true,
  created_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id)
);

ALTER TABLE tickets ADD COLUMN sla_policy_id uuid;
ALTER TABLE tickets ADD CONSTRAINT tickets_sla_policy_id_same_company FOREIGN KEY (company_id, sla_policy_id) REFERENCES sla_policies (company_id, id);
ALTER TABLE tickets ADD COLUMN response_due timestamptz, ADD COLUMN resolution_due timestamptz, ADD COLUMN first_response_at timestamptz,
  ADD COLUMN escalated_at timestamptz, ADD COLUMN rating smallint CHECK (rating IS NULL OR rating BETWEEN 1 AND 5),
  ADD COLUMN rating_comment text NOT NULL DEFAULT '' CHECK (length(rating_comment) <= 2000), ADD COLUMN rated_at timestamptz,
  ADD COLUMN rating_token text, ADD COLUMN contact_email text NOT NULL DEFAULT '' CHECK (length(contact_email) <= 200);
ALTER TABLE tickets DROP CONSTRAINT IF EXISTS tickets_channel_check;
ALTER TABLE tickets ADD CONSTRAINT tickets_channel_check CHECK (channel IN ('phone', 'whatsapp', 'office', 'portal', 'field', 'email', 'other'));
CREATE UNIQUE INDEX tickets_rating_token_idx ON tickets (rating_token) WHERE rating_token IS NOT NULL;

-- inbound channels (e-mail forwarding service / WhatsApp webhook) find the company by this token
ALTER TABLE companies ADD COLUMN inbound_token text;
CREATE UNIQUE INDEX companies_inbound_token_idx ON companies (inbound_token) WHERE inbound_token IS NOT NULL;

-- +goose StatementBegin
-- inbound_lookup: the company of an inbound token (callable before any tenant is known; returns ids only)
CREATE FUNCTION inbound_lookup(p_token text) RETURNS TABLE (tenant_id uuid, company_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT c.tenant_id, c.id FROM companies c JOIN tenants t ON t.id = c.tenant_id
  WHERE c.inbound_token = p_token AND c.is_active AND t.is_active AND tenant_jobs_ok(t.id) AND length(p_token) >= 32
$$;
-- rating_lookup: the company of a ticket rating link
CREATE FUNCTION rating_lookup(p_token text) RETURNS TABLE (tenant_id uuid, company_id uuid, ticket_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT k.tenant_id, k.company_id, k.id FROM tickets k JOIN tenants t ON t.id = k.tenant_id
  WHERE k.rating_token = p_token AND t.is_active AND length(p_token) >= 32
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION inbound_lookup(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION rating_lookup(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION inbound_lookup(text) TO paxve_app;
GRANT EXECUTE ON FUNCTION rating_lookup(text) TO paxve_app;

-- ======================= automation =======================
CREATE TABLE automation_rules (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  name         text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 160),
  model        text NOT NULL CHECK (model IN ('invoice', 'ticket', 'subscription', 'sales_order', 'task', 'service_job', 'item_stock', 'vehicle')),
  trigger      text NOT NULL CHECK (trigger IN ('created', 'updated', 'scheduled')),
  watch_fields text[] NOT NULL DEFAULT '{}',            -- updated: only when one of these changed (empty = any change)
  conditions   jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(conditions) = 'array' AND jsonb_array_length(conditions) <= 20),
  actions      jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(actions) = 'array' AND jsonb_array_length(actions) BETWEEN 1 AND 10),
  repeat_days  int NOT NULL DEFAULT 0 CHECK (repeat_days BETWEEN 0 AND 365), -- scheduled: act again on the same record after N days (0 = once)
  template_key text NOT NULL DEFAULT '',
  is_active    boolean NOT NULL DEFAULT true,
  paused_reason text NOT NULL DEFAULT '',
  run_count    bigint NOT NULL DEFAULT 0,
  last_run_at  timestamptz,
  created_by   uuid DEFAULT app_user(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id),
  CHECK (trigger = 'scheduled' OR model NOT IN ('item_stock', 'vehicle'))
);
CREATE INDEX automation_rules_active_idx ON automation_rules (company_id, model, trigger) WHERE is_active;

CREATE TABLE automation_events (
  id           bigserial PRIMARY KEY,
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  model        text NOT NULL,
  event        text NOT NULL CHECK (event IN ('created', 'updated')),
  record_id    uuid NOT NULL,
  changed      text[] NOT NULL DEFAULT '{}',
  created_at   timestamptz NOT NULL DEFAULT now(),
  processed_at timestamptz,
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id)
);
CREATE INDEX automation_events_due_idx ON automation_events (tenant_id, id) WHERE processed_at IS NULL;

CREATE TABLE automation_runs (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  rule_id     uuid NOT NULL,
  record_id   uuid,
  record_label text NOT NULL DEFAULT '',
  status      text NOT NULL CHECK (status IN ('done', 'skipped', 'error', 'dry')),
  detail      jsonb NOT NULL DEFAULT '{}',
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT automation_runs_rule_id_same_company FOREIGN KEY (company_id, rule_id) REFERENCES automation_rules (company_id, id) ON DELETE CASCADE,
  UNIQUE (company_id, id)
);
CREATE INDEX automation_runs_rule_idx ON automation_runs (rule_id, record_id, created_at DESC);
CREATE INDEX automation_runs_company_idx ON automation_runs (company_id, created_at DESC);

-- tags set by people or by rules (any record type)
CREATE TABLE record_tags (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  entity_type text NOT NULL CHECK (entity_type ~ '^[a-z_]{2,40}$'),
  entity_id   uuid NOT NULL,
  tag         text NOT NULL CHECK (length(trim(tag)) BETWEEN 1 AND 40),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, entity_type, entity_id, tag)
);

-- +goose StatementBegin
-- automation_enqueue: queues "created" / "updated" events of a table for the automation worker, only when the
-- company has an active rule for it, and never for changes made by the automation itself (loop guard).
CREATE FUNCTION automation_enqueue() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_model text := TG_ARGV[0];
  v_event text := CASE WHEN TG_OP = 'INSERT' THEN 'created' ELSE 'updated' END;
  v_changed text[] := '{}';
BEGIN
  IF coalesce(current_setting('paxve.automation', true), '') = 'on' THEN
    RETURN NULL;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM automation_rules r WHERE r.company_id = NEW.company_id AND r.model = v_model AND r.trigger = v_event AND r.is_active) THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'UPDATE' THEN
    SELECT coalesce(array_agg(n.key ORDER BY n.key), '{}') INTO v_changed
    FROM jsonb_each(to_jsonb(NEW)) n JOIN jsonb_each(to_jsonb(OLD)) o ON o.key = n.key
    WHERE n.value IS DISTINCT FROM o.value AND n.key NOT IN ('updated_at', 'search');
    IF cardinality(v_changed) = 0 THEN
      RETURN NULL;
    END IF;
  END IF;
  INSERT INTO automation_events (tenant_id, company_id, model, event, record_id, changed)
  VALUES (NEW.tenant_id, NEW.company_id, v_model, v_event, NEW.id, v_changed);
  RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER invoices_automation AFTER INSERT OR UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION automation_enqueue('invoice');
CREATE TRIGGER tickets_automation AFTER INSERT OR UPDATE ON tickets FOR EACH ROW EXECUTE FUNCTION automation_enqueue('ticket');
CREATE TRIGGER subscriptions_automation AFTER INSERT OR UPDATE ON subscriptions FOR EACH ROW EXECUTE FUNCTION automation_enqueue('subscription');
CREATE TRIGGER sales_orders_automation AFTER INSERT OR UPDATE ON sales_orders FOR EACH ROW EXECUTE FUNCTION automation_enqueue('sales_order');
CREATE TRIGGER project_tasks_automation AFTER INSERT OR UPDATE ON project_tasks FOR EACH ROW EXECUTE FUNCTION automation_enqueue('task');
CREATE TRIGGER service_jobs_automation AFTER INSERT OR UPDATE ON service_jobs FOR EACH ROW EXECUTE FUNCTION automation_enqueue('service_job');

-- ======================= fleet =======================
ALTER TABLE vehicles ADD COLUMN kind text NOT NULL DEFAULT 'van' CHECK (kind IN ('car', 'van', 'truck', 'motorbike', 'bus', 'machine', 'other'));
ALTER TABLE vehicles ADD COLUMN make text NOT NULL DEFAULT '' CHECK (length(make) <= 60);
ALTER TABLE vehicles ADD COLUMN model text NOT NULL DEFAULT '' CHECK (length(model) <= 60);
ALTER TABLE vehicles ADD COLUMN year int CHECK (year IS NULL OR year BETWEEN 1950 AND 2100);
ALTER TABLE vehicles ADD COLUMN vin text NOT NULL DEFAULT '' CHECK (length(vin) <= 40);
ALTER TABLE vehicles ADD COLUMN fuel_type text NOT NULL DEFAULT 'diesel' CHECK (fuel_type IN ('diesel', 'gasoline', 'electric', 'hybrid', 'lpg', 'other'));
ALTER TABLE vehicles ADD COLUMN odometer numeric(12, 1) NOT NULL DEFAULT 0 CHECK (odometer >= 0);
ALTER TABLE vehicles ADD COLUMN fuel_norm numeric(8, 2) NOT NULL DEFAULT 0 CHECK (fuel_norm >= 0); -- expected litres / 100 km (0 = learn from history)
ALTER TABLE vehicles ADD COLUMN purchase_date date;
ALTER TABLE vehicles ADD COLUMN license_expiry date;
ALTER TABLE vehicles ADD COLUMN insurance_expiry date;
ALTER TABLE vehicles ADD COLUMN inspection_expiry date;
ALTER TABLE vehicles ADD COLUMN cost_center_id uuid;
ALTER TABLE vehicles ADD CONSTRAINT vehicles_cost_center_id_same_company FOREIGN KEY (company_id, cost_center_id) REFERENCES cost_centers (company_id, id);

ALTER TABLE companies ADD COLUMN fleet_fuel_account_id uuid, ADD COLUMN fleet_service_account_id uuid;
ALTER TABLE companies ADD CONSTRAINT companies_fleet_fuel_fk FOREIGN KEY (tenant_id, fleet_fuel_account_id) REFERENCES accounts (tenant_id, id);
ALTER TABLE companies ADD CONSTRAINT companies_fleet_service_fk FOREIGN KEY (tenant_id, fleet_service_account_id) REFERENCES accounts (tenant_id, id);

CREATE TABLE fleet_fuel_logs (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  vehicle_id  uuid NOT NULL,
  log_date    date NOT NULL,
  odometer    numeric(12, 1) NOT NULL CHECK (odometer >= 0),
  liters      numeric(10, 2) NOT NULL CHECK (liters > 0),
  amount      numeric(20, 4) NOT NULL DEFAULT 0 CHECK (amount >= 0),
  currency    char(3) REFERENCES currencies (code),
  full_tank   boolean NOT NULL DEFAULT true,
  station     text NOT NULL DEFAULT '' CHECK (length(station) <= 120),
  driver_id   uuid REFERENCES users (id),
  notes       text NOT NULL DEFAULT '' CHECK (length(notes) <= 1000),
  consumption numeric(8, 2),                 -- litres / 100 km since the previous full tank
  anomaly     text NOT NULL DEFAULT '',       -- '' | high_consumption | odometer_back
  expense_id  uuid,
  status      text NOT NULL DEFAULT 'posted' CHECK (status IN ('posted', 'void')),
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT fleet_fuel_logs_vehicle_id_same_company FOREIGN KEY (company_id, vehicle_id) REFERENCES vehicles (company_id, id),
  FOREIGN KEY (tenant_id, expense_id) REFERENCES expenses (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX fleet_fuel_logs_vehicle_idx ON fleet_fuel_logs (vehicle_id, odometer);

CREATE TABLE fleet_service_plans (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  vehicle_id  uuid NOT NULL,
  task        text NOT NULL CHECK (length(trim(task)) BETWEEN 1 AND 160),
  every_km    numeric(12, 1) CHECK (every_km IS NULL OR every_km > 0),
  every_days  int CHECK (every_days IS NULL OR every_days > 0),
  last_km     numeric(12, 1),
  last_date   date,
  is_active   boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT fleet_service_plans_vehicle_id_same_company FOREIGN KEY (company_id, vehicle_id) REFERENCES vehicles (company_id, id),
  UNIQUE (company_id, id),
  CHECK (every_km IS NOT NULL OR every_days IS NOT NULL)
);

CREATE TABLE fleet_service_log (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id  uuid NOT NULL,
  vehicle_id  uuid NOT NULL,
  plan_id     uuid,
  done_date   date NOT NULL,
  odometer    numeric(12, 1) CHECK (odometer IS NULL OR odometer >= 0),
  task        text NOT NULL CHECK (length(trim(task)) BETWEEN 1 AND 160),
  amount      numeric(20, 4) NOT NULL DEFAULT 0 CHECK (amount >= 0),
  currency    char(3) REFERENCES currencies (code),
  vendor      text NOT NULL DEFAULT '' CHECK (length(vendor) <= 120),
  notes       text NOT NULL DEFAULT '' CHECK (length(notes) <= 1000),
  expense_id  uuid,
  status      text NOT NULL DEFAULT 'posted' CHECK (status IN ('posted', 'void')),
  created_by  uuid DEFAULT app_user(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT fleet_service_log_vehicle_id_same_company FOREIGN KEY (company_id, vehicle_id) REFERENCES vehicles (company_id, id),
  CONSTRAINT fleet_service_log_plan_id_same_company FOREIGN KEY (company_id, plan_id) REFERENCES fleet_service_plans (company_id, id),
  FOREIGN KEY (tenant_id, expense_id) REFERENCES expenses (tenant_id, id),
  UNIQUE (company_id, id)
);
CREATE INDEX fleet_service_log_vehicle_idx ON fleet_service_log (vehicle_id, done_date DESC);

-- other expenses can be charged to a vehicle too (tolls, washing, fines…)
CREATE TABLE vehicle_costs (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  vehicle_id uuid NOT NULL,
  expense_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT vehicle_costs_vehicle_id_same_company FOREIGN KEY (company_id, vehicle_id) REFERENCES vehicles (company_id, id),
  FOREIGN KEY (tenant_id, expense_id) REFERENCES expenses (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (expense_id)
);

-- ---------- row security, grants and the audit trail ----------
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['projects', 'project_stages', 'project_milestones', 'project_tasks', 'timesheets', 'project_links',
                           'service_jobs', 'service_job_parts', 'sla_policies', 'automation_rules', 'automation_events', 'automation_runs',
                           'record_tags', 'fleet_fuel_logs', 'fleet_service_plans', 'fleet_service_log', 'vehicle_costs'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
    IF t NOT IN ('automation_events', 'automation_runs', 'service_job_parts', 'record_tags') THEN
      EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
    END IF;
  END LOOP;
END
$$;
-- +goose StatementEnd
GRANT USAGE ON SEQUENCE automation_events_id_seq TO paxve_app;
GRANT DELETE ON project_stages, timesheets, project_links, service_job_parts, automation_rules, automation_events, record_tags, vehicle_costs TO paxve_app;

-- +goose Down
DROP TRIGGER IF EXISTS invoices_automation ON invoices;
DROP TRIGGER IF EXISTS tickets_automation ON tickets;
DROP TRIGGER IF EXISTS subscriptions_automation ON subscriptions;
DROP TRIGGER IF EXISTS sales_orders_automation ON sales_orders;
DROP TABLE IF EXISTS vehicle_costs, fleet_service_log, fleet_service_plans, fleet_fuel_logs, record_tags, automation_runs, automation_events,
  automation_rules, service_job_parts, service_jobs, project_links, timesheets, project_tasks, project_milestones, project_stages, projects, sla_policies CASCADE;
DROP FUNCTION IF EXISTS automation_enqueue();
DROP FUNCTION IF EXISTS inbound_lookup(text);
DROP FUNCTION IF EXISTS rating_lookup(text);
ALTER TABLE tickets DROP COLUMN IF EXISTS sla_policy_id, DROP COLUMN IF EXISTS response_due, DROP COLUMN IF EXISTS resolution_due,
  DROP COLUMN IF EXISTS first_response_at, DROP COLUMN IF EXISTS escalated_at, DROP COLUMN IF EXISTS rating, DROP COLUMN IF EXISTS rating_comment,
  DROP COLUMN IF EXISTS rated_at, DROP COLUMN IF EXISTS rating_token, DROP COLUMN IF EXISTS contact_email;
ALTER TABLE companies DROP COLUMN IF EXISTS inbound_token, DROP COLUMN IF EXISTS fleet_fuel_account_id, DROP COLUMN IF EXISTS fleet_service_account_id;
ALTER TABLE employees DROP COLUMN IF EXISTS hourly_cost;
