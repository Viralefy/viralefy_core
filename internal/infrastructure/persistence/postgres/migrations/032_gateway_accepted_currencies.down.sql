BEGIN;
DROP INDEX IF EXISTS idx_gateways_active_ccy;
ALTER TABLE payment_gateways DROP COLUMN IF EXISTS accepted_currencies;
COMMIT;
