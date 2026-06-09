BEGIN;
DROP TABLE IF EXISTS user_journeys;
DROP INDEX IF EXISTS idx_user_events_type_time;
DROP INDEX IF EXISTS idx_user_events_user;
DROP INDEX IF EXISTS idx_user_events_visitor;
DROP TABLE IF EXISTS user_events;
COMMIT;
