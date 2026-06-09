BEGIN;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS notif_prefs JSONB NOT NULL DEFAULT '{"order_updates":true,"marketing":false,"reviews":true,"cart_recovery":true}'::jsonb;

COMMIT;
