-- Phase 7 audit fixes: customers matched on the full phone number, sales-order deposits reserved for their order,
-- insurance claims cancelled by till refunds (and settled at their booking rate), a write-off account for rejected
-- claims, provider signatures on inbound e-mail / WhatsApp, signing codes and link expiry for e-sign.
-- +goose Up

-- phone_key: the national number (digits, without 00 / +961 and the trunk 0): 03 123 456, +961 3 123456 → 3123456.
-- +goose StatementBegin
CREATE FUNCTION phone_key(p text) RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
  SELECT regexp_replace(regexp_replace(regexp_replace(regexp_replace(coalesce(p, ''), '[^0-9]', '', 'g'), '^00', ''),
    '^961([0-9]{7,8})$', '\1'), '^0', '')
$$;
-- +goose StatementEnd
CREATE INDEX customers_phone_key_idx ON customers (company_id, phone_key(phone));

-- a deposit stays reserved for its order until that order's invoice uses it, or it is released (order finished /
-- cancelled: kept as customer credit or refunded)
ALTER TABLE so_deposits ADD COLUMN released_at timestamptz,
  ADD COLUMN released_how text CHECK (released_how IN ('invoiced', 'credit', 'refund'));
DROP TRIGGER IF EXISTS so_deposits_immutable ON so_deposits;
-- deposits taken before this fix were already applied oldest-first: they are released as they are
UPDATE so_deposits SET released_at = now(), released_how = 'credit';
-- +goose StatementBegin
CREATE FUNCTION so_deposit_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'deposits cannot be deleted' USING ERRCODE = 'PXIMM';
  END IF;
  IF OLD.released_at IS NOT NULL
     OR (to_jsonb(NEW) - 'released_at' - 'released_how') <> (to_jsonb(OLD) - 'released_at' - 'released_how') THEN
    RAISE EXCEPTION 'a deposit can only be released once' USING ERRCODE = 'PXIMM';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER so_deposits_guard BEFORE UPDATE OR DELETE ON so_deposits FOR EACH ROW EXECUTE FUNCTION so_deposit_guard();

-- insurance claims: refunded at the till (part or all), the rate the receivable was booked at
ALTER TABLE pharma_claims DROP CONSTRAINT IF EXISTS pharma_claims_status_check;
ALTER TABLE pharma_claims ADD CONSTRAINT pharma_claims_status_check CHECK (status IN ('pending', 'submitted', 'paid', 'rejected', 'cancelled'));
ALTER TABLE pharma_claims ADD COLUMN refunded numeric(20, 4) NOT NULL DEFAULT 0 CHECK (refunded >= 0),
  ADD COLUMN rate numeric(30, 10);
ALTER TABLE pharma_claims ADD CONSTRAINT pharma_claims_refunded_max CHECK (refunded <= amount);

-- rejected claims are written off to their own account; inbound provider signing secrets
ALTER TABLE companies ADD COLUMN claims_writeoff_account_id uuid,
  ADD COLUMN inbound_wa_secret text NOT NULL DEFAULT '' CHECK (length(inbound_wa_secret) <= 200),
  ADD COLUMN inbound_mail_key text NOT NULL DEFAULT '' CHECK (length(inbound_mail_key) <= 200);
ALTER TABLE companies ADD CONSTRAINT companies_claims_writeoff_fk FOREIGN KEY (tenant_id, claims_writeoff_account_id) REFERENCES accounts (tenant_id, id);

-- e-sign: optional signing code, and links that stop working 30 days after the request is finished
ALTER TABLE sign_requests ADD COLUMN require_otp boolean NOT NULL DEFAULT false;
ALTER TABLE sign_signers ADD COLUMN otp_hash text NOT NULL DEFAULT '' CHECK (otp_hash = '' OR otp_hash ~ '^[0-9a-f]{64}$'),
  ADD COLUMN otp_expires timestamptz,
  ADD COLUMN otp_tries int NOT NULL DEFAULT 0,
  ADD COLUMN otp_verified_at timestamptz;
ALTER TABLE sign_events DROP CONSTRAINT IF EXISTS sign_events_event_check;
ALTER TABLE sign_events ADD CONSTRAINT sign_events_event_check CHECK (event IN ('created', 'sent', 'viewed', 'signed', 'declined', 'reminded',
  'completed', 'cancelled', 'expired', 'confirmed', 'verified'));
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION sign_lookup(p_token text) RETURNS TABLE (tenant_id uuid, company_id uuid, signer_id uuid, request_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT s.tenant_id, s.company_id, s.id, s.request_id FROM sign_signers s JOIN tenants t ON t.id = s.tenant_id
  JOIN sign_requests q ON q.id = s.request_id
  WHERE s.token = p_token AND t.is_active AND length(p_token) >= 32
    AND (q.status IN ('draft', 'sent') OR q.updated_at > now() - interval '30 days')
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION sign_lookup(p_token text) RETURNS TABLE (tenant_id uuid, company_id uuid, signer_id uuid, request_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT s.tenant_id, s.company_id, s.id, s.request_id FROM sign_signers s JOIN tenants t ON t.id = s.tenant_id
  WHERE s.token = p_token AND t.is_active AND length(p_token) >= 32
$$;
-- +goose StatementEnd
ALTER TABLE sign_events DROP CONSTRAINT IF EXISTS sign_events_event_check;
DELETE FROM sign_events WHERE event = 'verified';
ALTER TABLE sign_events ADD CONSTRAINT sign_events_event_check CHECK (event IN ('created', 'sent', 'viewed', 'signed', 'declined', 'reminded',
  'completed', 'cancelled', 'expired', 'confirmed'));
ALTER TABLE sign_signers DROP COLUMN IF EXISTS otp_verified_at, DROP COLUMN IF EXISTS otp_tries, DROP COLUMN IF EXISTS otp_expires, DROP COLUMN IF EXISTS otp_hash;
ALTER TABLE sign_requests DROP COLUMN IF EXISTS require_otp;
ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_claims_writeoff_fk;
ALTER TABLE companies DROP COLUMN IF EXISTS inbound_mail_key, DROP COLUMN IF EXISTS inbound_wa_secret, DROP COLUMN IF EXISTS claims_writeoff_account_id;
ALTER TABLE pharma_claims DROP CONSTRAINT IF EXISTS pharma_claims_refunded_max;
ALTER TABLE pharma_claims DROP COLUMN IF EXISTS rate, DROP COLUMN IF EXISTS refunded;
ALTER TABLE pharma_claims DROP CONSTRAINT IF EXISTS pharma_claims_status_check;
UPDATE pharma_claims SET status = 'rejected' WHERE status = 'cancelled';
ALTER TABLE pharma_claims ADD CONSTRAINT pharma_claims_status_check CHECK (status IN ('pending', 'submitted', 'paid', 'rejected'));
DROP TRIGGER IF EXISTS so_deposits_guard ON so_deposits;
DROP FUNCTION IF EXISTS so_deposit_guard();
ALTER TABLE so_deposits DROP COLUMN IF EXISTS released_how, DROP COLUMN IF EXISTS released_at;
CREATE TRIGGER so_deposits_immutable BEFORE UPDATE OR DELETE ON so_deposits FOR EACH ROW EXECUTE FUNCTION forbid_change();
DROP INDEX IF EXISTS customers_phone_key_idx;
DROP FUNCTION IF EXISTS phone_key(text);
