-- PHASE-9 §4.2 — Auth extraction. Tabelas usadas pelo viralefy_auth.
--
-- Mesmo Postgres compartilhado entre core e auth (decisão §6 do MD).
-- Core é dono da migration (single source of DDL); auth lê schema mas não
-- aplica DDL.
--
-- 3 tabelas novas:
--   refresh_tokens     — sessões de refresh (rotação 30d)
--   revoked_jtis       — hot-set de revogação consultado pelo dispatcher
--   password_resets    — tokens de reset (TTL 1h, single-use)
--
-- Todas com índices por TTL pra cleanup eficiente via cron e por lookup
-- crítico (jti, token_hash, refresh_token_hash).

-- ---------------------------------------------------------------------------
-- refresh_tokens
--
-- Política de rotação: cada /refresh emite token NOVO e revoga o anterior
-- (anti-replay). subject pode ser user OU admin — exatamente um dos dois é
-- not null. Constraint XOR garantida via CHECK.
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id                  TEXT PRIMARY KEY,
    -- token_hash: SHA256 do token bruto. Token bruto NUNCA persistido.
    token_hash          TEXT NOT NULL UNIQUE,
    user_id             TEXT REFERENCES users(id) ON DELETE CASCADE,
    admin_id            TEXT REFERENCES admins(id) ON DELETE CASCADE,
    issued_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at          TIMESTAMPTZ NOT NULL,
    revoked_at          TIMESTAMPTZ,
    -- Rotação encadeada: quando este token é usado em /refresh, gravamos o
    -- ID do filho aqui pra auditoria (anti-replay forense).
    replaced_by_id      TEXT REFERENCES refresh_tokens(id),
    -- Context de issue (IP, UA) pra investigação.
    issue_ip            TEXT,
    issue_user_agent    TEXT,
    CONSTRAINT refresh_tokens_subject_xor CHECK (
        (user_id IS NOT NULL AND admin_id IS NULL) OR
        (user_id IS NULL AND admin_id IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user
    ON refresh_tokens(user_id, revoked_at) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_admin
    ON refresh_tokens(admin_id, revoked_at) WHERE admin_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_expires
    ON refresh_tokens(expires_at) WHERE revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- revoked_jtis — hot-set
--
-- Cada row é um access token revogado. Consulta crítica é por jti:
--   SELECT 1 FROM revoked_jtis WHERE jti=$1 AND expires_at > now()
--
-- O dispatcher Rust faz bootstrap completo no startup e LISTEN/NOTIFY pra
-- updates incrementais. TTL = exp do token original; cleanup via cron
-- (já existe idempotency_cleanup_cron pattern — clonar pra revoked_jtis).
CREATE TABLE IF NOT EXISTS revoked_jtis (
    jti                 TEXT PRIMARY KEY,
    expires_at          TIMESTAMPTZ NOT NULL,
    revoked_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_reason      TEXT,
    revoked_by_admin_id TEXT REFERENCES admins(id),
    revoked_by_user_id  TEXT REFERENCES users(id)
);

CREATE INDEX IF NOT EXISTS idx_revoked_jtis_expires_at
    ON revoked_jtis(expires_at);

-- ---------------------------------------------------------------------------
-- password_resets
--
-- Token de reset (one-shot, TTL 1h). token_hash = SHA256 do token bruto
-- enviado por email; bruto nunca persistido.
CREATE TABLE IF NOT EXISTS password_resets (
    id                  TEXT PRIMARY KEY,
    token_hash          TEXT NOT NULL UNIQUE,
    user_id             TEXT REFERENCES users(id) ON DELETE CASCADE,
    admin_id            TEXT REFERENCES admins(id) ON DELETE CASCADE,
    requested_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at          TIMESTAMPTZ NOT NULL,
    used_at             TIMESTAMPTZ,
    requested_ip        TEXT,
    requested_user_agent TEXT,
    CONSTRAINT password_resets_subject_xor CHECK (
        (user_id IS NOT NULL AND admin_id IS NULL) OR
        (user_id IS NULL AND admin_id IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_password_resets_user
    ON password_resets(user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_password_resets_admin
    ON password_resets(admin_id) WHERE admin_id IS NOT NULL;
