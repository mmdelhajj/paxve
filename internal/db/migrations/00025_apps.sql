-- Module R: Odoo-style apps per company (switch modules on/off) and the business type that tints the UI.

-- +goose Up
ALTER TABLE companies
  ADD COLUMN apps text[] NOT NULL DEFAULT '{gen,acc,col,msg,stk,pur,net,sat,pos,hr,crm}',
  ADD COLUMN business_type text NOT NULL DEFAULT 'generator'
    CHECK (business_type IN ('generator', 'internet', 'satellite'));

ALTER TABLE companies ADD CONSTRAINT companies_apps_known
  CHECK (apps <@ ARRAY['gen','acc','col','msg','stk','pur','net','sat','pos','hr','crm']::text[]);

-- Existing companies: core apps plus whatever they already use.
UPDATE companies c SET apps = ARRAY(SELECT unnest(ARRAY['gen','acc','col']
    || CASE WHEN EXISTS (SELECT 1 FROM message_providers x WHERE x.company_id = c.id) THEN ARRAY['msg'] ELSE '{}' END
    || CASE WHEN EXISTS (SELECT 1 FROM items x WHERE x.company_id = c.id) THEN ARRAY['stk'] ELSE '{}' END
    || CASE WHEN EXISTS (SELECT 1 FROM suppliers x WHERE x.company_id = c.id)
              OR EXISTS (SELECT 1 FROM expenses x WHERE x.company_id = c.id) THEN ARRAY['pur'] ELSE '{}' END
    || CASE WHEN EXISTS (SELECT 1 FROM routers x WHERE x.company_id = c.id) THEN ARRAY['net'] ELSE '{}' END
    || CASE WHEN EXISTS (SELECT 1 FROM pos_registers x WHERE x.company_id = c.id) THEN ARRAY['pos'] ELSE '{}' END
    || CASE WHEN EXISTS (SELECT 1 FROM employees x WHERE x.company_id = c.id) THEN ARRAY['hr'] ELSE '{}' END
    || CASE WHEN EXISTS (SELECT 1 FROM crm_leads x WHERE x.company_id = c.id)
              OR EXISTS (SELECT 1 FROM sales_orders x WHERE x.company_id = c.id) THEN ARRAY['crm'] ELSE '{}' END));

UPDATE companies c SET business_type = 'internet'
 WHERE EXISTS (SELECT 1 FROM routers x WHERE x.company_id = c.id)
   AND NOT EXISTS (SELECT 1 FROM generators x WHERE x.company_id = c.id);

-- +goose Down
ALTER TABLE companies DROP CONSTRAINT companies_apps_known;
ALTER TABLE companies DROP COLUMN apps, DROP COLUMN business_type;
