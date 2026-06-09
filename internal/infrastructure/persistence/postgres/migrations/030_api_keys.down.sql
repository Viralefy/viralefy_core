BEGIN;

DROP INDEX IF EXISTS idx_api_keys_owner;
DROP INDEX IF EXISTS idx_api_keys_active;
DROP TABLE IF EXISTS api_keys;

COMMIT;
