BEGIN;
ALTER TABLE users
  DROP COLUMN IF EXISTS twofa_prompt_last_dismissed_at,
  DROP COLUMN IF EXISTS twofa_prompt_dismissed_count;
ALTER TABLE admins DROP COLUMN IF EXISTS requires_2fa;
DROP TABLE IF EXISTS user_2fa;
DROP TABLE IF EXISTS admin_2fa;
COMMIT;
