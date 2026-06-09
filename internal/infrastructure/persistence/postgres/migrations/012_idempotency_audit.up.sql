-- Idempotency keys + audit log.
--
-- idempotency_keys: armazena a resposta original de mutations identificadas
-- por header `Idempotency-Key`. Re-tries com a mesma key voltam a mesma
-- resposta byte-a-byte, evitando double-charge no checkout/recovery-request.
-- TTL de 24h é suficiente pra cobrir retries de gateways e refresh do user.
--
-- audit_log: imutável (não tem update/delete via app). Toda mutação admin
-- de plano/categoria/moeda escreve uma linha com before/after JSONB.

BEGIN;

CREATE TABLE IF NOT EXISTS idempotency_keys (
    key             TEXT PRIMARY KEY,
    method          TEXT NOT NULL,
    path            TEXT NOT NULL,
    -- Hash do corpo do request — re-tries com a mesma key mas corpo
    -- diferente devolvem 409 (RFC draft idempotency-header §2.5).
    request_hash    TEXT NOT NULL,
    response_status INT  NOT NULL,
    response_body   BYTEA NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '24 hours'
);
CREATE INDEX IF NOT EXISTS idx_idempotency_expires ON idempotency_keys(expires_at);

CREATE TABLE IF NOT EXISTS audit_log (
    id          TEXT PRIMARY KEY,
    actor_type  TEXT NOT NULL,  -- admin | system
    actor_id    TEXT NOT NULL,  -- admin.id ou 'system' / 'webhook' / 'cron'
    action      TEXT NOT NULL,  -- create | update | delete
    target_type TEXT NOT NULL,  -- plan | category | currency | gateway
    target_id   TEXT NOT NULL,
    before_data JSONB,           -- estado anterior (null em creates)
    after_data  JSONB,           -- estado novo (null em deletes)
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb, -- IP, user agent, motivo
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_audit_target  ON audit_log(target_type, target_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_actor   ON audit_log(actor_type, actor_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at DESC);

COMMIT;
