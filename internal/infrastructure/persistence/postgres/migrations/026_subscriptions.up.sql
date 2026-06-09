-- Subscription system (Fase 6.3).
--
-- User pode assinar um plano em intervalo recorrente (default mensal). O
-- cron de renovação varre subscriptions ativas com next_billing_at <= NOW()
-- e dispara CheckoutService.Checkout via interface pública — gera order
-- pending + payment_url. User paga normalmente; cada renovação que falha
-- N vezes (>=3) auto-cancela a assinatura.
--
-- Invariantes:
--   * UNIQUE(user_id, plan_id, status) DEFERRABLE — evita que o mesmo
--     user tenha duas subs ATIVAS pro mesmo plano. Cancelar + reassinar
--     OK porque o status muda.
--   * idx_subscriptions_due acelera o cron — partial index só nos rows
--     active (volume baixo, escaneia rápido).
--   * orders.subscription_id linka uma order gerada por renovação à sub
--     que a gerou — usado pra histórico e auditoria.

BEGIN;

CREATE TABLE IF NOT EXISTS subscriptions (
    id              TEXT        PRIMARY KEY,
    user_id         TEXT        NOT NULL REFERENCES users(id),
    plan_id         TEXT        NOT NULL REFERENCES plans(id),
    status          TEXT        NOT NULL CHECK (status IN ('active','paused','cancelled')) DEFAULT 'active',
    interval        TEXT        NOT NULL DEFAULT 'month',
    next_billing_at TIMESTAMPTZ NOT NULL,
    cancelled_at    TIMESTAMPTZ,
    failed_payments INT         NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, plan_id, status) DEFERRABLE INITIALLY DEFERRED
);

CREATE INDEX IF NOT EXISTS idx_subscriptions_due
    ON subscriptions(next_billing_at)
    WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_subscriptions_user
    ON subscriptions(user_id);

ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS subscription_id TEXT REFERENCES subscriptions(id);

COMMIT;
