-- Customer portal: each customer has a secret link (no login) showing their bills and payments.

-- +goose Up

ALTER TABLE customers ADD COLUMN portal_token text;
UPDATE customers SET portal_token = replace(gen_random_uuid()::text, '-', '') || replace(gen_random_uuid()::text, '-', '');
ALTER TABLE customers
  ALTER COLUMN portal_token SET DEFAULT (replace(gen_random_uuid()::text, '-', '') || replace(gen_random_uuid()::text, '-', '')),
  ALTER COLUMN portal_token SET NOT NULL,
  ADD CONSTRAINT customers_portal_token_len CHECK (length(portal_token) >= 32);
CREATE UNIQUE INDEX customers_portal_token_key ON customers (portal_token);

-- The portal runs before any tenant is known: one narrow lookup from token to (tenant, company, customer).
-- Closed customers and suspended organisations get nothing.
-- +goose StatementBegin
CREATE FUNCTION portal_lookup(p_token text)
RETURNS TABLE (tenant_id uuid, company_id uuid, customer_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT c.tenant_id, c.company_id, c.id
  FROM customers c JOIN tenants t ON t.id = c.tenant_id
  WHERE c.portal_token = p_token AND c.status <> 'closed' AND t.is_active
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION portal_lookup(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION portal_lookup(text) TO paxve_app;

-- +goose Down
DROP FUNCTION IF EXISTS portal_lookup(text);
ALTER TABLE customers DROP COLUMN IF EXISTS portal_token;
