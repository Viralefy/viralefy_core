-- 043_user_deletion_drop_fk — separa a forense do hard-delete.
--
-- Migration 042 deixou user_deletion_requests.user_id como TEXT NOT
-- NULL REFERENCES users(id). Isso bloqueia o cron: pra registrar
-- status='executed' precisa preservar a row APÓS DELETE FROM users —
-- FK quebra a tx.
--
-- Decisão: dropar a FK. A linha de auditoria sobrevive com user_id
-- órfão (string que aponta pra UUID já apagado), mantendo o registro
-- "este UUID foi excluído em <data>" pra fins forenses.
--
-- user_id continua NOT NULL — preserva o invariante "toda request
-- nasceu de um user real". UNIQUE também fica (idempotência da
-- UPSERT no service).

BEGIN;

ALTER TABLE user_deletion_requests
    DROP CONSTRAINT IF EXISTS user_deletion_requests_user_id_fkey;

COMMIT;
