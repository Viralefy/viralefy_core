-- 040_proof_storage_key — preparação para migração dos comprovantes legados
-- de base64-inline (data:URL armazenada em orders.proof_url) para object
-- storage S3-compat (MinIO local / R2). Coexistência:
--
--   proof_storage_key NULL  → leitura cai no fluxo legacy de proof_url
--                              (pode ser data:URL base64 ou http URL externa)
--   proof_storage_key NOT NULL → leitura presigna MinIO/R2 ignorando proof_url
--
-- O migrator (cmd/migrate-proofs) varre rows com proof_url começando em
-- "data:" e popula proof_storage_key + bucket após upload. Idempotente:
-- WHERE proof_storage_key IS NULL filtra rows já migradas.
--
-- Não dropa proof_url: rollback do app code precisa do base64 ainda
-- presente. Cleanup definitivo (UPDATE ... SET proof_url=NULL WHERE
-- proof_storage_key IS NOT NULL) fica para migration futura, após 1 ciclo
-- de release confirmado em prod.
BEGIN;

ALTER TABLE orders
  ADD COLUMN IF NOT EXISTS proof_storage_key VARCHAR(255);

-- Index parcial: queries de admin/leitura filtram NOT NULL pra pegar
-- só migradas; queries de migrator filtram IS NULL pra achar pendentes.
-- Postgres usa o partial index nos dois lados (NULL e NOT NULL via
-- index-only scan no first; full scan no NULL filter é OK porque migrador
-- roda offline e batch).
CREATE INDEX IF NOT EXISTS idx_orders_proof_storage_key
  ON orders(proof_storage_key)
  WHERE proof_storage_key IS NOT NULL;

COMMIT;
