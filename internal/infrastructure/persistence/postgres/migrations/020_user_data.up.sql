-- LGPD/GDPR: "Manage my data" (Fase 5.2).
--
-- user_deletion_requests: pedidos de exclusão de conta. O usuário tem 30
-- dias de janela pra cancelar antes do hard-delete físico. Status:
--   - pending   → na fila, executes_at no futuro
--   - cancelled → usuário desistiu
--   - executed  → hard-delete já rolou (cron futuro)
--
-- Esquema 1:1 com users via UNIQUE(user_id) — só uma request ativa por
-- vez; UPSERT no service re-arma o timer quando o usuário muda de ideia
-- depois de cancelar.
--
-- O cron de execução física do delete fica como tech debt (registrado no
-- workflow). Aqui só persistimos a intenção.
--
-- users.deleted_at: marca soft-delete. Set quando o cron executa; permite
-- login/checkout/etc. checarem deleted_at IS NULL antes de servir.

BEGIN;

CREATE TABLE IF NOT EXISTS user_deletion_requests (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL UNIQUE REFERENCES users(id),
    requested_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    executes_at   TIMESTAMPTZ NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','cancelled','executed')),
    reason        TEXT
);

CREATE INDEX IF NOT EXISTS idx_user_deletion_pending
    ON user_deletion_requests(executes_at) WHERE status = 'pending';

ALTER TABLE users ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

COMMIT;
