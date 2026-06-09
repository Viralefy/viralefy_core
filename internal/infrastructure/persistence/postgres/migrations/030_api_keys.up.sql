-- B2B API keys (Fase 7.5).
--
-- Permite que parceiros consultem catálogo público + status de pedidos via
-- header X-API-Key. A coluna key_hash guarda SHA-256 da key plain (32 bytes
-- base32) — a key plain é mostrada UMA vez no momento da criação.
--
-- revoked_at preserva a row como audit (não DELETE). Index parcial cobre o
-- caminho quente do middleware: lookup por hash apenas em keys ativas.
--
-- V2 (não coberto aqui): rate-limit per-key (tabela api_key_usage) e billing
-- por chamada. Esse scaffold é só auth + revogação.

BEGIN;

CREATE TABLE IF NOT EXISTS api_keys (
    id              TEXT PRIMARY KEY,
    key_hash        TEXT NOT NULL UNIQUE,
    label           TEXT NOT NULL,
    owner_user_id   TEXT REFERENCES users(id),
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_api_keys_active
    ON api_keys(key_hash) WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_api_keys_owner
    ON api_keys(owner_user_id);

COMMIT;
