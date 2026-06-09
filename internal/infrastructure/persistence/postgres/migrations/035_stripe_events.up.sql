-- 035_stripe_events — log de event_ids processados pelo webhook Stripe.
-- Defesa contra double-fire: Stripe re-entrega webhook em 5xx (3 tentativas
-- com backoff). Se a primeira tentativa demorou pra retornar 200 (mas o
-- MarkOrderPaid foi disparado), a segunda chega ANTES de orders.status
-- virar 'paid'. PaymentReceiver.MarkOrderPaid já é idempotente via guard,
-- mas duplicar email/ticket no caminho rápido é desperdício.
--
-- received_at default NOW. Sem TTL aqui — Stripe re-entrega por até ~3 dias;
-- limpamos via cron de retenção genérico (>90d, alinhado com event_retention_cron).
BEGIN;

CREATE TABLE IF NOT EXISTS stripe_events_processed (
  event_id    TEXT PRIMARY KEY,
  event_type  TEXT NOT NULL,
  order_id    TEXT,
  received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_stripe_events_received
  ON stripe_events_processed(received_at DESC);

COMMIT;
