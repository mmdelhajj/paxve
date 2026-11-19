-- Final stage: service desk app (customer requests / complaints / faults) and generator maintenance plans.
-- +goose Up

ALTER TABLE companies DROP CONSTRAINT companies_apps_known;
ALTER TABLE companies ADD CONSTRAINT companies_apps_known
  CHECK (apps <@ ARRAY['gen','acc','col','msg','stk','pur','net','sat','pos','hr','crm','svc']::text[]);
ALTER TABLE companies ALTER COLUMN apps SET DEFAULT '{gen,acc,col,msg,stk,pur,net,sat,pos,hr,crm,svc}';
UPDATE companies SET apps = apps || ARRAY['svc'] WHERE NOT 'svc' = ANY (apps);

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['customers', 'subscriptions', 'generators'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = t || '_company_id_key') THEN
      EXECUTE format('CREATE UNIQUE INDEX %I ON %I (company_id, id)', t || '_company_id_key', t);
      EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I UNIQUE USING INDEX %I', t, t || '_company_id_key', t || '_company_id_key');
    END IF;
  END LOOP;
END
$$;
-- +goose StatementEnd

-- ---------- service requests (tickets) ----------
CREATE TABLE tickets (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id      uuid NOT NULL,
  number          int NOT NULL,
  customer_id     uuid,
  subscription_id uuid,
  category        text NOT NULL DEFAULT 'other' CHECK (category IN ('outage', 'no_internet', 'slow', 'no_signal', 'meter', 'billing',
                    'new_connection', 'move', 'disconnect', 'complaint', 'other')),
  priority        text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  status          text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'in_progress', 'waiting', 'done', 'cancelled')),
  subject         text NOT NULL CHECK (length(trim(subject)) > 0),
  description     text NOT NULL DEFAULT '',
  contact_phone   text NOT NULL DEFAULT '',
  channel         text NOT NULL DEFAULT 'phone' CHECK (channel IN ('phone', 'whatsapp', 'office', 'portal', 'field', 'other')),
  assigned_to     uuid REFERENCES users (id),
  due_at          timestamptz,
  resolved_at     timestamptz,
  resolution      text NOT NULL DEFAULT '',
  created_by      uuid DEFAULT app_user(),
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT tickets_customer_id_same_company FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
  CONSTRAINT tickets_subscription_id_same_company FOREIGN KEY (company_id, subscription_id) REFERENCES subscriptions (company_id, id),
  UNIQUE (tenant_id, id),
  UNIQUE (company_id, id),
  UNIQUE (company_id, number)
);
CREATE INDEX tickets_open_idx ON tickets (company_id, status, created_at DESC);
CREATE INDEX tickets_customer_idx ON tickets (company_id, customer_id);

CREATE TABLE ticket_notes (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id uuid NOT NULL,
  ticket_id  uuid NOT NULL,
  body       text NOT NULL CHECK (length(trim(body)) > 0),
  created_by uuid DEFAULT app_user(),
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT ticket_notes_ticket_id_same_company FOREIGN KEY (company_id, ticket_id) REFERENCES tickets (company_id, id)
);
CREATE INDEX ticket_notes_ticket_idx ON ticket_notes (ticket_id, created_at);

-- ---------- generator maintenance ----------
CREATE TABLE maintenance_plans (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id      uuid NOT NULL,
  generator_id    uuid NOT NULL,
  task            text NOT NULL CHECK (length(trim(task)) > 0),
  every_hours     numeric(10, 1) CHECK (every_hours IS NULL OR every_hours > 0),
  every_days      int CHECK (every_days IS NULL OR every_days > 0),
  last_done_date  date,
  last_done_hours numeric(12, 1),
  is_active       boolean NOT NULL DEFAULT true,
  notes           text NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT maintenance_plans_generator_id_same_company FOREIGN KEY (company_id, generator_id) REFERENCES generators (company_id, id),
  UNIQUE (company_id, id),
  CHECK (every_hours IS NOT NULL OR every_days IS NOT NULL)
);

CREATE TABLE maintenance_log (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL DEFAULT app_tenant() REFERENCES tenants (id),
  company_id   uuid NOT NULL,
  generator_id uuid NOT NULL,
  plan_id      uuid,
  done_date    date NOT NULL,
  hour_meter   numeric(12, 1) CHECK (hour_meter IS NULL OR hour_meter >= 0),
  task         text NOT NULL CHECK (length(trim(task)) > 0),
  cost         numeric(18, 4) NOT NULL DEFAULT 0 CHECK (cost >= 0),
  currency     char(3),
  notes        text NOT NULL DEFAULT '',
  created_by   uuid DEFAULT app_user(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, company_id) REFERENCES companies (tenant_id, id),
  CONSTRAINT maintenance_log_generator_id_same_company FOREIGN KEY (company_id, generator_id) REFERENCES generators (company_id, id),
  CONSTRAINT maintenance_log_plan_id_same_company FOREIGN KEY (company_id, plan_id) REFERENCES maintenance_plans (company_id, id)
);
CREATE INDEX maintenance_log_gen_idx ON maintenance_log (company_id, generator_id, done_date DESC);

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['tickets', 'ticket_notes', 'maintenance_plans', 'maintenance_log'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    EXECUTE format('CREATE POLICY company_scope ON %I AS RESTRICTIVE FOR ALL USING (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[]))) WITH CHECK (((SELECT app_company()) IS NULL OR company_id = (SELECT app_company())) AND ((SELECT app_companies()) IS NULL OR company_id = ANY ((SELECT app_companies())::uuid[])))', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO paxve_app', t);
    EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION audit_row()', t || '_audit', t);
  END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
