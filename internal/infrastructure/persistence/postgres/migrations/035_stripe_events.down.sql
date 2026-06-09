BEGIN;
DROP INDEX IF EXISTS idx_stripe_events_received;
DROP TABLE IF EXISTS stripe_events_processed;
COMMIT;
