-- Large tenant for performance work: 50k customers, 600k invoices (12 months), ~500k payments,
-- ~2.2M journal lines. Run as superuser on the THROWAWAY perf database only.
\set ON_ERROR_STOP on
\timing on
SET session_replication_role = replica;
SELECT id AS cid, tenant_id AS tid FROM companies LIMIT 1 \gset
UPDATE tenants SET plan = 'pro', trial_ends_at = NULL WHERE id = :'tid'::uuid;
SELECT id AS plan FROM plans WHERE company_id = :'cid'::uuid LIMIT 1 \gset
SELECT ar_account_id AS ar, default_revenue_account_id AS rev FROM companies WHERE id = :'cid'::uuid \gset
SELECT id AS cash_m, account_id AS cash FROM payment_methods WHERE company_id = :'cid'::uuid AND name = 'Cash USD' \gset
SELECT id AS uid FROM users WHERE tenant_id = :'tid'::uuid LIMIT 1 \gset

INSERT INTO areas (tenant_id, company_id, name)
SELECT :'tid'::uuid, :'cid'::uuid, 'Area ' || g FROM generate_series(1, 40) g;

INSERT INTO customers (tenant_id, company_id, code, name, phone, area_id, status)
SELECT :'tid'::uuid, :'cid'::uuid, 'C' || g, 'Customer ' || g || ' زبون', '70' || lpad(g::text, 6, '0'),
       (SELECT id FROM areas WHERE company_id = :'cid'::uuid ORDER BY id OFFSET (g % 40) LIMIT 1), 'active'
FROM generate_series(1, 50000) g;

INSERT INTO subscriptions (tenant_id, company_id, customer_id, plan_id, start_date, status)
SELECT :'tid'::uuid, :'cid'::uuid, id, :'plan'::uuid, '2025-01-01', 'active' FROM customers WHERE company_id = :'cid'::uuid;

-- 12 monthly invoices per subscription
CREATE TEMP TABLE gen_inv AS
SELECT uuidv7() AS id, uuidv7() AS je, s.customer_id, s.id AS sub, to_char(d, 'YYYY-MM') AS period, d::date AS issue,
       row_number() OVER (ORDER BY d, s.id) AS n
FROM subscriptions s CROSS JOIN generate_series('2025-12-01'::date, '2026-11-01'::date, interval '1 month') d
WHERE s.company_id = :'cid'::uuid;

INSERT INTO journal_entries (id, tenant_id, company_id, number, entry_date, memo, status, source, created_by, posted_by, posted_at)
SELECT je, :'tid'::uuid, :'cid'::uuid, n, issue + 4, 'Invoice ' || n, 'posted', 'invoice', :'uid'::uuid, :'uid'::uuid, issue + 4 FROM gen_inv;
INSERT INTO journal_lines (tenant_id, company_id, entry_id, line_no, account_id, description, currency, debit, credit, rate, base_debit, base_credit)
SELECT :'tid'::uuid, :'cid'::uuid, je, 1, :'ar'::uuid, 'Invoice ' || n, 'USD', 20, 0, 1, 20, 0 FROM gen_inv
UNION ALL
SELECT :'tid'::uuid, :'cid'::uuid, je, 2, :'rev'::uuid, 'Invoice ' || n, 'USD', 0, 20, 1, 0, 20 FROM gen_inv;

INSERT INTO invoices (id, tenant_id, company_id, number, customer_id, subscription_id, period, issue_date, due_date, currency, total, paid,
                      status, journal_entry_id, created_by, kind)
SELECT id, :'tid'::uuid, :'cid'::uuid, n, customer_id, sub, period, issue + 4, issue + 14, 'USD', 20,
       CASE WHEN period <= '2026-09' THEN 20 ELSE 0 END, CASE WHEN period <= '2026-09' THEN 'paid' ELSE 'open' END, je, :'uid'::uuid, 'invoice'
FROM gen_inv;
INSERT INTO invoice_lines (tenant_id, company_id, invoice_id, line_no, kind, description, quantity, unit_price, amount)
SELECT :'tid'::uuid, :'cid'::uuid, id, 1, 'fee', 'Monthly fee ' || period, 1, 20, 20 FROM gen_inv;

-- payments for the paid months (one per invoice)
CREATE TEMP TABLE gen_pay AS
SELECT uuidv7() AS id, uuidv7() AS je, i.id AS inv, i.customer_id, i.issue + 12 AS pdate, row_number() OVER (ORDER BY i.n) AS n,
       (SELECT max(n) FROM gen_inv) AS base
FROM gen_inv i WHERE i.period <= '2026-09';

INSERT INTO journal_entries (id, tenant_id, company_id, number, entry_date, memo, status, source, created_by, posted_by, posted_at)
SELECT je, :'tid'::uuid, :'cid'::uuid, base + n, pdate, 'Payment ' || n, 'posted', 'payment', :'uid'::uuid, :'uid'::uuid, pdate FROM gen_pay;
INSERT INTO journal_lines (tenant_id, company_id, entry_id, line_no, account_id, description, currency, debit, credit, rate, base_debit, base_credit)
SELECT :'tid'::uuid, :'cid'::uuid, je, 1, :'cash'::uuid, 'Payment ' || n, 'USD', 20, 0, 1, 20, 0 FROM gen_pay
UNION ALL
SELECT :'tid'::uuid, :'cid'::uuid, je, 2, :'ar'::uuid, 'Payment ' || n, 'USD', 0, 20, 1, 0, 20 FROM gen_pay;
INSERT INTO payments (id, tenant_id, company_id, number, customer_id, payment_date, currency, amount, status, journal_entry_id, received_by, kind, ar_rate)
SELECT id, :'tid'::uuid, :'cid'::uuid, n, customer_id, pdate, 'USD', 20, 'posted', je, :'uid'::uuid, 'payment', 1 FROM gen_pay;
INSERT INTO payment_tenders (tenant_id, company_id, payment_id, method_id, currency, amount, rate)
SELECT :'tid'::uuid, :'cid'::uuid, id, :'cash_m'::uuid, 'USD', 20, 1 FROM gen_pay;
INSERT INTO payment_allocations (tenant_id, company_id, payment_id, invoice_id, amount)
SELECT :'tid'::uuid, :'cid'::uuid, id, inv, 20 FROM gen_pay;

-- counters, so the app continues the numbering
INSERT INTO journal_counters (tenant_id, company_id, last_number) VALUES (:'tid'::uuid, :'cid'::uuid, (SELECT count(*) FROM journal_entries WHERE company_id = :'cid'::uuid AND status = 'posted'))
ON CONFLICT (tenant_id, company_id) DO UPDATE SET last_number = excluded.last_number;
INSERT INTO doc_counters (tenant_id, company_id, doc_type, last_number) VALUES
  (:'tid'::uuid, :'cid'::uuid, 'invoice', (SELECT max(number) FROM invoices WHERE company_id = :'cid'::uuid)),
  (:'tid'::uuid, :'cid'::uuid, 'payment', (SELECT max(number) FROM payments WHERE company_id = :'cid'::uuid)),
  (:'tid'::uuid, :'cid'::uuid, 'customer', 50000)
ON CONFLICT (tenant_id, company_id, doc_type) DO UPDATE SET last_number = excluded.last_number;
SET session_replication_role = origin;
VACUUM ANALYZE;
SELECT (SELECT count(*) FROM customers) customers, (SELECT count(*) FROM invoices) invoices, (SELECT count(*) FROM payments) payments,
       (SELECT count(*) FROM journal_lines) lines, pg_size_pretty(pg_database_size(current_database())) size;
