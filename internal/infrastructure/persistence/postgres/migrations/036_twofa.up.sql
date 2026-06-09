-- 036_twofa — Two-factor authentication (TOTP RFC 6238).
--
-- Política (vide PHASE-7-PLAN §7.2):
--   - admin: obrigatório (admins.requires_2fa default TRUE). Login bloqueia
--     em partial_token até enroll + verify.
--   - user: opcional. Backend incrementa users.twofa_prompt_dismissed_count
--     a cada dismiss; front decide se mostra nag com base em (orders
--     paid+delivered) AND (dismissed_count < N OR último dismiss > 7d).
--
-- Secrets cifrados AES-256-GCM em rest com env TWOFA_ENCRYPTION_KEY.
-- Backup codes hashed bcrypt cost 10, comparison constant-time.
BEGIN;

CREATE TABLE IF NOT EXISTS admin_2fa (
  admin_id            TEXT PRIMARY KEY REFERENCES admins(id) ON DELETE CASCADE,
  secret_encrypted    TEXT NOT NULL,           -- AES-256-GCM(secret) hex-encoded
  backup_codes_hashed TEXT[] NOT NULL DEFAULT '{}'::TEXT[],
  enrolled_at         TIMESTAMPTZ,
  last_used_at        TIMESTAMPTZ,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS user_2fa (
  user_id             TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  secret_encrypted    TEXT NOT NULL,
  backup_codes_hashed TEXT[] NOT NULL DEFAULT '{}'::TEXT[],
  enrolled_at         TIMESTAMPTZ,
  last_used_at        TIMESTAMPTZ,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- requires_2fa controla se o login bloqueia em partial_token. Default
-- TRUE pra todos os admins existentes (backfill via UPDATE).
ALTER TABLE admins
  ADD COLUMN IF NOT EXISTS requires_2fa BOOLEAN NOT NULL DEFAULT TRUE;

-- Backfill: admins criados antes da feature ganham requires_2fa=TRUE.
UPDATE admins SET requires_2fa = TRUE WHERE requires_2fa IS DISTINCT FROM TRUE;

-- twofa_prompt_dismissed_count: usado pela lógica de nag do user.
-- Cooldown progressivo — dismiss > 5 vezes OU último > 7d = espera maior.
ALTER TABLE users
  ADD COLUMN IF NOT EXISTS twofa_prompt_dismissed_count INT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS twofa_prompt_last_dismissed_at TIMESTAMPTZ;

COMMIT;
