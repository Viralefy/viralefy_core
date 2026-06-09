-- Cart abandonment: marca quando um e-mail de "you forgot your checkout"
-- foi disparado, pra não enviar duas vezes. O cron CartAbandonmentCron
-- (internal/application/cart_abandonment_cron.go) varre orders pendentes
-- entre 1h e 24h com payment_url e dispara o lembrete.
--
-- Por que NULL como default: a maioria dos pedidos paga rápido (< 1h) e
-- nunca precisa do lembrete. NULL = nunca enviado; timestamp = enviado em.
BEGIN;

ALTER TABLE orders ADD COLUMN IF NOT EXISTS abandonment_email_sent_at TIMESTAMPTZ;

-- Índice parcial: só linhas elegíveis para varredura (status=pending e ainda
-- não notificadas). Mantém o índice pequeno e a query do cron um seek O(log n).
CREATE INDEX IF NOT EXISTS idx_orders_abandonment
    ON orders(status, created_at)
    WHERE status='pending' AND abandonment_email_sent_at IS NULL;

COMMIT;
