-- Refund / dispute admin (Fase 5.4).
--
-- Admin emite refund parcial ou total numa order paid. Política:
--   * refund_type='to_credits' — devolve o valor em saldo de créditos do
--     usuário (entrada no ledger). Único caminho 100% automatizado.
--   * refund_type='to_gateway' — placeholder: registra o pedido + external_ref
--     opcional, mas o estorno real depende de suporte do gateway (Woovi/Heleket
--     ainda não expostos no service). Fica logado pra reconciliação manual.
--
-- Invariante: SUM(order_refunds.refund_usd_cents) <= orders.amount_cents.
-- A coluna orders.refunded_usd_cents é cache somado (mesma invariante de
-- credits.balance_cents = SUM(transactions)). Cron de auditoria pode comparar.

BEGIN;

CREATE TABLE IF NOT EXISTS order_refunds (
    id                TEXT PRIMARY KEY,
    order_id          TEXT NOT NULL REFERENCES orders(id),
    refund_usd_cents  INT  NOT NULL CHECK (refund_usd_cents > 0),
    refund_type       TEXT NOT NULL CHECK (refund_type IN ('to_credits','to_gateway')),
    reason            TEXT,
    refunded_by       TEXT NOT NULL REFERENCES admins(id),
    external_ref      TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_refunds_order ON order_refunds(order_id);

ALTER TABLE orders ADD COLUMN IF NOT EXISTS refunded_usd_cents INT NOT NULL DEFAULT 0;

COMMIT;
