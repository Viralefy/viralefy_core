BEGIN;

ALTER TABLE orders DROP COLUMN IF EXISTS subscription_id;

DROP INDEX IF EXISTS idx_subscriptions_user;
DROP INDEX IF EXISTS idx_subscriptions_due;
DROP TABLE IF EXISTS subscriptions;

COMMIT;
