-- Module Q: indexes for large companies (50k customers, 600k bills, 2M+ journal lines measured).

-- +goose Up
-- JIT compilation costs up to a second per big report query on small servers and gains nothing for these
-- short queries: off for the application role.
ALTER ROLE paxve_app SET jit = off;
CREATE INDEX IF NOT EXISTS invoices_company_date_idx ON invoices (company_id, issue_date DESC, number DESC) INCLUDE (tenant_id, currency, total, paid, status, kind);
CREATE INDEX IF NOT EXISTS invoices_open_idx ON invoices (company_id, customer_id) INCLUDE (tenant_id, currency, total, paid, due_date, issue_date) WHERE status IN ('open', 'partial');
CREATE INDEX IF NOT EXISTS invoices_company_currency_idx ON invoices (company_id, currency);
CREATE INDEX IF NOT EXISTS payments_company_date_idx ON payments (company_id, payment_date DESC, number DESC) INCLUDE (tenant_id, currency, amount, status, kind);
CREATE INDEX IF NOT EXISTS payments_company_currency_idx ON payments (company_id, currency);
CREATE INDEX IF NOT EXISTS journal_entries_drafts_idx ON journal_entries (company_id, entry_date DESC, created_at DESC) WHERE status = 'draft';
CREATE INDEX IF NOT EXISTS journal_entries_posted_date_idx ON journal_entries (company_id, entry_date DESC, number DESC) WHERE status = 'posted';
CREATE INDEX IF NOT EXISTS journal_lines_company_account_idx ON journal_lines (company_id, account_id, entry_id);
CREATE INDEX IF NOT EXISTS subscriptions_company_status_idx ON subscriptions (company_id, status) INCLUDE (tenant_id);
CREATE INDEX IF NOT EXISTS customers_company_status_idx ON customers (company_id, status) INCLUDE (tenant_id, area_id);

-- +goose Down
ALTER ROLE paxve_app RESET jit;
DROP INDEX IF EXISTS customers_company_status_idx;
DROP INDEX IF EXISTS subscriptions_company_status_idx;
DROP INDEX IF EXISTS journal_lines_company_account_idx;
DROP INDEX IF EXISTS journal_entries_posted_date_idx;
DROP INDEX IF EXISTS journal_entries_drafts_idx;
DROP INDEX IF EXISTS payments_company_currency_idx;
DROP INDEX IF EXISTS payments_company_date_idx;
DROP INDEX IF EXISTS invoices_company_currency_idx;
DROP INDEX IF EXISTS invoices_open_idx;
DROP INDEX IF EXISTS invoices_company_date_idx;
