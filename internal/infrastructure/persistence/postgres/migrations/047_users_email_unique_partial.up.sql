-- 047_users_email_unique_partial — permitir reuso de email após soft-delete.
--
-- Contexto:
--   Bug em prod (2026-06-15): user `k3ntluk@gmail.com` foi soft-deletado em
--   2026-06-12, e ao tentar se recadastrar recebeu 409 CONFLICT. Root cause:
--     1) UNIQUE constraint `users_email_key` cobre TODOS os emails, inclusive
--        soft-deletados. INSERT do mesmo email viola.
--     2) `UserRepo.GetByEmail` (auth + core) NÃO filtra `deleted_at IS NULL`,
--        retorna o registro soft-deletado, e o check em `RegisterUser` (auth
--        e core) dispara `ErrConflict`.
--
-- Estratégia:
--   Troca constraint UNIQUE total por índice parcial `WHERE deleted_at IS NULL`.
--   Assim:
--     - email único entre users ATIVOS (regra de negócio mantida)
--     - emails de soft-deletados ficam "livres" pra reuso
--     - múltiplos soft-deletados com mesmo email são permitidos (caso já
--       hipotético no histórico, mas válido)
--
--   A correção do `GetByEmail` (filtrar deleted_at) é aplicada no código Go;
--   esta migration garante que o INSERT também não trave por unique violation.

BEGIN;

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_email_key;

CREATE UNIQUE INDEX IF NOT EXISTS users_email_active_key
    ON users (email)
    WHERE deleted_at IS NULL;

COMMIT;
