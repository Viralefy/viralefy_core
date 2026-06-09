BEGIN;

ALTER TABLE orders DROP COLUMN IF EXISTS refunded_usd_cents;
DROP INDEX IF EXISTS idx_refunds_order;
DROP TABLE IF EXISTS order_refunds;

COMMIT;
