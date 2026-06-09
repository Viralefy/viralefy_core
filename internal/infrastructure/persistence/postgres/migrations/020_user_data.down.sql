BEGIN;

ALTER TABLE users DROP COLUMN IF EXISTS deleted_at;

DROP INDEX IF EXISTS idx_user_deletion_pending;
DROP TABLE IF EXISTS user_deletion_requests;

COMMIT;
