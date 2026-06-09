BEGIN;

ALTER TABLE users DROP COLUMN IF EXISTS whatsapp_verified_at;
ALTER TABLE users DROP COLUMN IF EXISTS whatsapp_opt_in;
ALTER TABLE users DROP COLUMN IF EXISTS whatsapp_number;

COMMIT;
