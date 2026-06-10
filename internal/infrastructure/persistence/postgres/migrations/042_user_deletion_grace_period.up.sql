-- 041_user_deletion_grace_period — fechamento do loop de hard-delete LGPD.
--
-- Contexto:
--   Migration 020 criou user_deletion_requests só com a INTENÇÃO de
--   excluir. Cron de execução física ficou como tech debt
--   (`user_data_service.go:20-21`). LGPD Art. 18 IV obriga execução —
--   este migration habilita o cron `user-deletion-cron`.
--
-- Estratégia:
--   1. Estende status check pra incluir 'failed' (cron pode quebrar
--      em meio à cascata; precisamos marcar pra alerta sem bloquear
--      a fila inteira).
--   2. Adiciona `executed_at` e `error_message` em
--      `user_deletion_requests` pra forense (quando rodou, e o que
--      quebrou se quebrou).
--   3. Index novo por status='pending' AND executes_at <= NOW() —
--      hot-path do cron, evita seq scan na tabela inteira.
--   4. Anonimização de orders: adiciona `email_at_purchase` e
--      `name_at_purchase` (snapshot do dado fiscal pra atender
--      retenção 5y da Receita) + relaxa `user_id` pra nullable.
--      Quando o cron executar o hard-delete, faz UPDATE orders
--      SET user_id=NULL, e o snapshot de email/nome sobrevive
--      anonimizado conforme task pede.
--   5. Backfill: orders existentes ganham email_at_purchase e
--      name_at_purchase copiados de users (idempotente; NULL pra
--      órfãos eventuais).
--
-- Mantemos `executes_at` (nome legado da 020) em vez de renomear pra
-- `scheduled_for` — reescrever colunas existentes em prod custa caro
-- e quebra rollback; o cron lê por `executes_at`.

BEGIN;

-- 1) Status enum estendido
ALTER TABLE user_deletion_requests
    DROP CONSTRAINT IF EXISTS user_deletion_requests_status_check;
ALTER TABLE user_deletion_requests
    ADD CONSTRAINT user_deletion_requests_status_check
    CHECK (status IN ('pending','cancelled','executed','failed'));

-- 2) Colunas de forense
ALTER TABLE user_deletion_requests
    ADD COLUMN IF NOT EXISTS executed_at   TIMESTAMPTZ;
ALTER TABLE user_deletion_requests
    ADD COLUMN IF NOT EXISTS error_message TEXT;

-- 3) Index pro hot-path do cron
CREATE INDEX IF NOT EXISTS idx_user_deletion_due
    ON user_deletion_requests(executes_at)
    WHERE status = 'pending';

-- 4) Snapshot fiscal em orders + user_id nullable pra anonimização
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS email_at_purchase TEXT;
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS name_at_purchase  TEXT;

-- 5) Backfill snapshot — orders pre-existentes ficam consistentes pra
--    quando user for deletado. Best-effort: LEFT JOIN pra não falhar
--    se algum order tiver user_id órfão (não deveria, mas defesa).
UPDATE orders o
   SET email_at_purchase = COALESCE(o.email_at_purchase, u.email),
       name_at_purchase  = COALESCE(o.name_at_purchase,  u.name)
  FROM users u
 WHERE o.user_id = u.id
   AND (o.email_at_purchase IS NULL OR o.name_at_purchase IS NULL);

-- 6) Permite NULL em orders.user_id pra anonimização.
--    Constraint NOT NULL original era da 001_init — afrouxamos aqui,
--    mas o handler de checkout SEMPRE seta o user_id, então o caminho
--    quente não regride. Só o cron escreve NULL.
ALTER TABLE orders ALTER COLUMN user_id DROP NOT NULL;

COMMIT;
