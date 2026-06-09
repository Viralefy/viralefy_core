BEGIN;
DROP INDEX IF EXISTS idx_orders_target_country;
ALTER TABLE orders DROP COLUMN IF EXISTS target_country_code;
COMMIT;
