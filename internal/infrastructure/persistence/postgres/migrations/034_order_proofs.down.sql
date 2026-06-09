BEGIN;
DROP INDEX IF EXISTS idx_orders_proof_pending;
ALTER TABLE orders
  DROP COLUMN IF EXISTS proof_note,
  DROP COLUMN IF EXISTS proof_status,
  DROP COLUMN IF EXISTS proof_uploaded_at,
  DROP COLUMN IF EXISTS proof_url;
DROP INDEX IF EXISTS idx_order_proofs_order;
DROP TABLE IF EXISTS order_proofs;
COMMIT;
