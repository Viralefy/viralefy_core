-- Baseline + delivery metrics: segunda fonte de verdade pra confirmar que
-- o serviço foi efetivamente entregue pela API do gateway.
--
-- baseline_metrics  — snapshot antes do gateway começar (followers/likes/
--                     views/comments visíveis publicamente)
-- delivery_metrics  — snapshot N horas após o pedido virar 'paid'
-- *_captured_at     — quando cada snapshot foi tirado
-- *_source          — método: og_scrape | manual | api3p (3rd-party tool)
--
-- Comparar (delivery - baseline) com order.followers_qty / amount do plano
-- diz se a entrega bateu. Discrepâncias > 10% sobem alerta no admin.

BEGIN;

ALTER TABLE orders ADD COLUMN IF NOT EXISTS baseline_metrics      JSONB;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS baseline_captured_at  TIMESTAMPTZ;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS baseline_source       TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS delivery_metrics      JSONB;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS delivery_captured_at  TIMESTAMPTZ;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS delivery_source       TEXT;

CREATE INDEX IF NOT EXISTS idx_orders_baseline_captured
  ON orders(baseline_captured_at) WHERE baseline_captured_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_orders_delivery_captured
  ON orders(delivery_captured_at) WHERE delivery_captured_at IS NOT NULL;

COMMIT;
