-- Tracking data para anti-fraude + eventos de conversão (Meta CAPI,
-- Google Ads, TikTok Events API).
--
-- orders.tracking      — snapshot completo no momento do checkout
-- users.tracking_data  — landing inicial do usuário (first_landing)
--
-- Schema livre por jsonb; chaves esperadas (não exigidas):
--   utm_source, utm_medium, utm_campaign, utm_term, utm_content
--   fbclid, fbp, fbc, gclid, ttclid, msclkid
--   referrer, landing_url, landing_at
--   ip, user_agent, screen, viewport, language, timezone
--   client_id (UUID gerado no first-touch, persistido em cookie)
--
-- Idempotente.

BEGIN;

ALTER TABLE orders ADD COLUMN IF NOT EXISTS tracking JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE users  ADD COLUMN IF NOT EXISTS tracking_data JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Indexes pra anti-fraude: lookup rápido por fbclid/gclid (campanha de
-- vinda) e por client_id (mesmo "visitante" comprando duas vezes).
CREATE INDEX IF NOT EXISTS idx_orders_tracking_fbclid
  ON orders ((tracking->>'fbclid')) WHERE tracking->>'fbclid' IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_orders_tracking_gclid
  ON orders ((tracking->>'gclid')) WHERE tracking->>'gclid' IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_orders_tracking_client
  ON orders ((tracking->>'client_id')) WHERE tracking->>'client_id' IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_orders_tracking_utm_campaign
  ON orders ((tracking->>'utm_campaign')) WHERE tracking->>'utm_campaign' IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_users_tracking_client
  ON users ((tracking_data->>'client_id')) WHERE tracking_data->>'client_id' IS NOT NULL;

COMMIT;
