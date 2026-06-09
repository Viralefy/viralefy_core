BEGIN;

DROP TABLE IF EXISTS referral_rewards;

DROP INDEX IF EXISTS idx_users_referred_by;

ALTER TABLE users DROP COLUMN IF EXISTS referred_by_user_id;
ALTER TABLE users DROP COLUMN IF EXISTS referral_code;

COMMIT;
