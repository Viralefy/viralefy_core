BEGIN;
DROP INDEX IF EXISTS idx_users_tracking_client;
DROP INDEX IF EXISTS idx_orders_tracking_utm_campaign;
DROP INDEX IF EXISTS idx_orders_tracking_client;
DROP INDEX IF EXISTS idx_orders_tracking_gclid;
DROP INDEX IF EXISTS idx_orders_tracking_fbclid;
ALTER TABLE users  DROP COLUMN IF EXISTS tracking_data;
ALTER TABLE orders DROP COLUMN IF EXISTS tracking;
COMMIT;
