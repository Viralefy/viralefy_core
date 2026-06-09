BEGIN;

DROP INDEX IF EXISTS idx_orders_abandonment;
ALTER TABLE orders DROP COLUMN IF EXISTS abandonment_email_sent_at;

COMMIT;
