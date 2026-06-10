-- Rollback 040_proof_storage_key. proof_url preserva o conteúdo legacy
-- (data:URL ou key) porque o migrador NÃO apagou; rollback é seguro.
BEGIN;

DROP INDEX IF EXISTS idx_orders_proof_storage_key;

ALTER TABLE orders
  DROP COLUMN IF EXISTS proof_storage_key;

COMMIT;
