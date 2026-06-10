-- Down: drop em ordem reversa. Mantém a coluna analytics_consent caso
-- queira manter histórico parcial — mas aqui dropamos pra ser literal.
BEGIN;

ALTER TABLE user_events
  DROP COLUMN IF EXISTS analytics_consent;

DROP INDEX IF EXISTS idx_user_consent_log_visitor;
DROP INDEX IF EXISTS idx_user_consent_log_user;
DROP TABLE IF EXISTS user_consent_log;

COMMIT;
