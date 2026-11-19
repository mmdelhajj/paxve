-- Module S leftovers:
--   * employees and fixed assets carry a cost center (payroll and depreciation expense lines are tagged with it)
--   * a bounced / returned cheque debit remembers its cheque, so it is shown and voided as a cheque, not as a refund

-- +goose Up
ALTER TABLE employees ADD COLUMN cost_center_id uuid;
ALTER TABLE employees ADD CONSTRAINT employees_cost_center_id_same_company
  FOREIGN KEY (company_id, cost_center_id) REFERENCES cost_centers (company_id, id);

ALTER TABLE assets ADD COLUMN cost_center_id uuid;
ALTER TABLE assets ADD CONSTRAINT assets_cost_center_id_same_company
  FOREIGN KEY (company_id, cost_center_id) REFERENCES cost_centers (company_id, id);

ALTER TABLE invoices ADD COLUMN cheque_id uuid;
ALTER TABLE invoices ADD CONSTRAINT invoices_cheque_id_same_company
  FOREIGN KEY (company_id, cheque_id) REFERENCES cheques (company_id, id);
CREATE INDEX invoices_cheque_idx ON invoices (cheque_id) WHERE cheque_id IS NOT NULL;

-- existing cheque debits: their notes read "<event> · cheque <no> (#<number>)"
ALTER TABLE invoices DISABLE TRIGGER invoices_guard;
UPDATE invoices i SET cheque_id = c.id
FROM cheques c
WHERE i.kind = 'refund' AND i.cheque_id IS NULL AND c.company_id = i.company_id AND c.direction = 'in'
  AND i.notes LIKE '%· cheque ' || c.cheque_no || ' (#' || c.number || ')%';
ALTER TABLE invoices ENABLE TRIGGER invoices_guard;

-- +goose Down
DROP INDEX IF EXISTS invoices_cheque_idx;
ALTER TABLE invoices DROP COLUMN IF EXISTS cheque_id;
ALTER TABLE assets DROP COLUMN IF EXISTS cost_center_id;
ALTER TABLE employees DROP COLUMN IF EXISTS cost_center_id;
