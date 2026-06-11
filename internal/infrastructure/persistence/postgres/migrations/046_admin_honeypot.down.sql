BEGIN;

DROP INDEX IF EXISTS idx_admin_honeypot_attempted_at;
DROP INDEX IF EXISTS idx_admin_honeypot_actor_target_action;
DROP TABLE IF EXISTS admin_honeypot_log;

COMMIT;
