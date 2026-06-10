BEGIN;

-- Reverter NOT NULL em user_id é destrutivo se houver linhas
-- anonimizadas (UPDATE ... SET user_id=NULL). Em prod, rollback exige
-- decisão humana sobre o que fazer com órfãos — deliberadamente NÃO
-- restauramos NOT NULL aqui pra evitar falha de constraint.

DROP INDEX IF EXISTS idx_user_deletion_due;

ALTER TABLE user_deletion_requests
    DROP COLUMN IF EXISTS executed_at,
    DROP COLUMN IF EXISTS error_message;

ALTER TABLE user_deletion_requests
    DROP CONSTRAINT IF EXISTS user_deletion_requests_status_check;
ALTER TABLE user_deletion_requests
    ADD CONSTRAINT user_deletion_requests_status_check
    CHECK (status IN ('pending','cancelled','executed'));

ALTER TABLE orders
    DROP COLUMN IF EXISTS email_at_purchase,
    DROP COLUMN IF EXISTS name_at_purchase;

COMMIT;
