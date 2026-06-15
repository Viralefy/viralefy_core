-- Rollback: volta ao UNIQUE total. Se existirem duplicatas (active + soft-deleted
-- com mesmo email), o rollback FALHA — operador precisa decidir manualmente qual
-- registro manter (e.g. renomear o email do soft-deletado para `<email>+deleted`).

BEGIN;

DROP INDEX IF EXISTS users_email_active_key;

ALTER TABLE users ADD CONSTRAINT users_email_key UNIQUE (email);

COMMIT;
