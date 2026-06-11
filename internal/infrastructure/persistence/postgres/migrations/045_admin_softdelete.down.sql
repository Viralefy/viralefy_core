BEGIN;

DROP INDEX IF EXISTS idx_invoices_deleted_at;
DROP INDEX IF EXISTS idx_orders_deleted_at;

ALTER TABLE invoices
  DROP COLUMN IF EXISTS delete_reason,
  DROP COLUMN IF EXISTS deleted_by_admin_id,
  DROP COLUMN IF EXISTS deleted_at;

ALTER TABLE orders
  DROP COLUMN IF EXISTS delete_reason,
  DROP COLUMN IF EXISTS deleted_by_admin_id,
  DROP COLUMN IF EXISTS deleted_at;

ALTER TABLE users
  DROP COLUMN IF EXISTS delete_reason,
  DROP COLUMN IF EXISTS deleted_by_admin_id;

COMMIT;
