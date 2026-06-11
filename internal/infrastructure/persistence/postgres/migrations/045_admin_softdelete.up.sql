-- 045: soft-delete em orders + invoices.
--
-- Política de delete administrativo (2026-06-11):
--
--   * Admin com permissão `admins:manage` pode executar SOFT delete em
--     order/invoice/user. Grava deleted_at, deleted_by_admin_id e
--     delete_reason; a linha continua no DB. Listagens da loja (front,
--     /v1/me/*) filtram por deleted_at IS NULL — o registro some pra
--     o cliente. Painel admin LISTA INCLUSIVE soft-deleted, badge
--     "Deleted" pra deixar óbvio.
--
--   * Apenas role=superadmin pode executar HARD delete (DELETE row).
--     Antes de aplicar, a UI sugere fazer soft delete primeiro.
--
-- users já tem `deleted_at TIMESTAMPTZ` (migration 020). Aqui adicionamos
-- também `deleted_by_admin_id` + `delete_reason` pra trilha de auditoria.
-- Coluna existente em users vira opcional pelo cron (LGPD self-delete) —
-- nesses casos deleted_by_admin_id fica NULL.

BEGIN;

-- ----- orders -------------------------------------------------------------

ALTER TABLE orders
  ADD COLUMN IF NOT EXISTS deleted_at           TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS deleted_by_admin_id  TEXT REFERENCES admins(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS delete_reason        TEXT;

-- Índice parcial: queries normais (deleted_at IS NULL) ignoram esse índice;
-- o painel admin filtra com `deleted_at IS NOT NULL` pra ver só os deletados.
CREATE INDEX IF NOT EXISTS idx_orders_deleted_at
  ON orders(deleted_at) WHERE deleted_at IS NOT NULL;

-- ----- invoices -----------------------------------------------------------

ALTER TABLE invoices
  ADD COLUMN IF NOT EXISTS deleted_at           TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS deleted_by_admin_id  TEXT REFERENCES admins(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS delete_reason        TEXT;

CREATE INDEX IF NOT EXISTS idx_invoices_deleted_at
  ON invoices(deleted_at) WHERE deleted_at IS NOT NULL;

-- ----- users (já tem deleted_at desde 020) --------------------------------

ALTER TABLE users
  ADD COLUMN IF NOT EXISTS deleted_by_admin_id  TEXT REFERENCES admins(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS delete_reason        TEXT;

COMMIT;
