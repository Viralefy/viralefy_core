BEGIN;

DROP INDEX IF EXISTS idx_fraud_blocks_until;
DROP TABLE IF EXISTS fraud_blocks;

DROP INDEX IF EXISTS idx_fraud_signals_severity;
DROP INDEX IF EXISTS idx_fraud_signals_actor;
DROP TABLE IF EXISTS fraud_signals;

COMMIT;
