-- Module T, part 2: country localization packs. The company remembers which pack it was set up with
-- (country, e-invoicing scheme, fiscal year start, the label of its tax number on documents).

-- +goose Up
ALTER TABLE companies ADD COLUMN country text NOT NULL DEFAULT '' CHECK (country ~ '^([A-Z]{2})?$');
ALTER TABLE companies ADD COLUMN l10n text NOT NULL DEFAULT '';
ALTER TABLE companies ADD COLUMN fiscal_year_start text NOT NULL DEFAULT '01-01' CHECK (fiscal_year_start ~ '^[01][0-9]-[0-3][0-9]$');
ALTER TABLE companies ADD COLUMN einvoice text NOT NULL DEFAULT '' CHECK (einvoice IN ('', 'zatca', 'eta'));
ALTER TABLE companies ADD COLUMN tax_id_label text NOT NULL DEFAULT '';
ALTER TABLE companies ADD COLUMN invoice_title text NOT NULL DEFAULT '';
ALTER TABLE companies ADD COLUMN legal_mentions text NOT NULL DEFAULT '';

-- companies set up with the Lebanese chart before packs existed
UPDATE companies c SET country = 'LB', l10n = 'lebanon'
WHERE c.l10n = '' AND EXISTS (SELECT 1 FROM accounts a WHERE a.company_id = c.id AND a.code = '4427')
  AND EXISTS (SELECT 1 FROM accounts a WHERE a.company_id = c.id AND a.code = '411');

-- +goose Down
ALTER TABLE companies DROP COLUMN IF EXISTS legal_mentions;
ALTER TABLE companies DROP COLUMN IF EXISTS invoice_title;
ALTER TABLE companies DROP COLUMN IF EXISTS tax_id_label;
ALTER TABLE companies DROP COLUMN IF EXISTS einvoice;
ALTER TABLE companies DROP COLUMN IF EXISTS fiscal_year_start;
ALTER TABLE companies DROP COLUMN IF EXISTS l10n;
ALTER TABLE companies DROP COLUMN IF EXISTS country;
