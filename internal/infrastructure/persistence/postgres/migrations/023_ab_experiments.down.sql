BEGIN;

DROP INDEX IF EXISTS idx_ab_events_exp;
DROP TABLE IF EXISTS ab_events;
DROP TABLE IF EXISTS ab_assignments;
DROP TABLE IF EXISTS ab_experiments;

COMMIT;
