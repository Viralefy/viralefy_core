BEGIN;

-- Restaurar FK em rollback exige que NÃO haja rows com user_id órfão.
-- Em prod, isso falharia após o cron rodar. Por isso o ADD CONSTRAINT
-- ficaria com NOT VALID e validação manual posterior — comportamento
-- intencional pra evitar surpresa.
ALTER TABLE user_deletion_requests
    ADD CONSTRAINT user_deletion_requests_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id) NOT VALID;

COMMIT;
