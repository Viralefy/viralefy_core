-- 034_order_proofs — comprovantes de pagamento anexados pelo cliente após
-- depositar manualmente (PIX, crypto on-chain). Admin revisa em backoffice e
-- ativa a order quando confirma. Permitimos múltiplos uploads por order
-- (cliente reanexa screenshot melhor); o último prevalece em UI.
BEGIN;

CREATE TABLE IF NOT EXISTS order_proofs (
  id          TEXT PRIMARY KEY,
  order_id    TEXT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  file_url    TEXT NOT NULL,
  file_name   TEXT,
  mime_type   TEXT,
  size_bytes  INT,
  note        TEXT,
  uploaded_by TEXT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_order_proofs_order ON order_proofs(order_id, created_at DESC);

-- Denormaliza no order pra leitura barata no painel + cron de aging.
-- proof_status: NULL=sem comprovante; "pending"=anexado, aguarda admin;
-- "approved"=admin marcou pago (rollback fácil pra refund/dispute);
-- "rejected"=admin recusou (cliente reanexa).
ALTER TABLE orders
  ADD COLUMN IF NOT EXISTS proof_url          TEXT,
  ADD COLUMN IF NOT EXISTS proof_uploaded_at  TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS proof_status       TEXT,
  ADD COLUMN IF NOT EXISTS proof_note         TEXT;

CREATE INDEX IF NOT EXISTS idx_orders_proof_pending
  ON orders(proof_uploaded_at DESC)
  WHERE proof_status = 'pending';

COMMIT;
