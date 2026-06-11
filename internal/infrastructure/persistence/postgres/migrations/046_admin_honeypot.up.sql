-- 046: honeypot — superadmin é invisível pra admins normais.
--
-- Threat model: um admin com permissão admins:manage pode tentar:
--   * Listar admins (incluindo o superadmin) → vê quem tem mais poder
--   * Demover superadmin → tomar conta do sistema
--   * Deletar superadmin → ganhar exclusividade
--
-- Mitigação: superadmin aparece pro admin normal como role="manager"
-- (camuflado entre admins comuns). Tentativas de modificar/deletar viram
-- no-op + log nesta tabela. O REAL superadmin vê tudo na aba honeypot e
-- toma a ação corretiva (banir o admin malicioso).
--
-- Esta tabela TAMBÉM funciona como shadow-delete state: se existe uma row
-- com (actor_admin_id=X, target_admin_id=Y, action='delete'), então o
-- admin X NÃO VÊ MAIS o Y na listagem dele (ele pensa que apagou).
-- Outros admins (e o superadmin de verdade) continuam vendo Y normal.

BEGIN;

CREATE TABLE IF NOT EXISTS admin_honeypot_log (
  id              TEXT PRIMARY KEY,
  actor_admin_id  TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  target_admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  action          TEXT NOT NULL,             -- 'list_view'|'get'|'update_role'|'delete'
  attempted_role  TEXT,                       -- preenchido em update_role
  metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
  attempted_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Pra checar "actor X já deletou target Y?" em LIST/GET, índice composto:
CREATE INDEX IF NOT EXISTS idx_admin_honeypot_actor_target_action
  ON admin_honeypot_log(actor_admin_id, target_admin_id, action);

-- Pra superadmin ler em ordem cronológica reversa (UI: tentativas mais
-- recentes primeiro), índice por timestamp:
CREATE INDEX IF NOT EXISTS idx_admin_honeypot_attempted_at
  ON admin_honeypot_log(attempted_at DESC);

COMMIT;
