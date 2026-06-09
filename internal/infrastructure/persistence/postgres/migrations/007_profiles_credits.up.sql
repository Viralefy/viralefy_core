-- Perfis sociais do usuário (Instagram, TikTok…). Cada perfil é (plataforma, handle).
CREATE TABLE IF NOT EXISTS profiles (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    platform     TEXT NOT NULL,              -- 'instagram' | 'tiktok'
    handle       TEXT NOT NULL,              -- @ sem o arroba (normalizado)
    display_name TEXT NOT NULL DEFAULT '',
    verified     BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, platform, handle)
);
CREATE INDEX IF NOT EXISTS idx_profiles_user ON profiles(user_id);

-- Planos ganham plataforma e tipo de alvo.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS platform    TEXT NOT NULL DEFAULT 'instagram'; -- instagram | tiktok
ALTER TABLE plans ADD COLUMN IF NOT EXISTS target_type TEXT NOT NULL DEFAULT 'profile';   -- profile | publication

-- Pedidos: alvo (perfil ou URL de publicação), método de pagamento e créditos usados.
ALTER TABLE orders ADD COLUMN IF NOT EXISTS profile_id         TEXT REFERENCES profiles(id);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS publication_url    TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS payment_method     TEXT NOT NULL DEFAULT 'gateway'; -- gateway | credits
ALTER TABLE orders ADD COLUMN IF NOT EXISTS credits_used_cents INT  NOT NULL DEFAULT 0;

-- Conta de créditos por usuário (saldo em BRL cents — fonte de verdade do balance).
-- Atualizada por trigger? Não — em código, dentro da mesma transação do credit_transactions.
CREATE TABLE IF NOT EXISTS credit_accounts (
    user_id       TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    balance_cents BIGINT NOT NULL DEFAULT 0,
    currency      TEXT NOT NULL DEFAULT 'BRL',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Ledger imutável (apenas INSERT). entrada/saída por type, signed amount.
-- balance_after_cents é snapshot pra auditoria — invariante: SUM(amount_cents) = balance_cents.
CREATE TABLE IF NOT EXISTS credit_transactions (
    id                  TEXT PRIMARY KEY,
    user_id             TEXT NOT NULL REFERENCES users(id),
    type                TEXT NOT NULL,                 -- recharge | spend | refund | adjustment
    amount_cents        BIGINT NOT NULL,               -- positivo = entrada, negativo = saída
    balance_after_cents BIGINT NOT NULL,
    currency            TEXT NOT NULL DEFAULT 'BRL',
    order_id            TEXT REFERENCES orders(id),
    invoice_id          TEXT,
    description         TEXT NOT NULL DEFAULT '',
    metadata            JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_credit_tx_user    ON credit_transactions(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_credit_tx_invoice ON credit_transactions(invoice_id);
CREATE INDEX IF NOT EXISTS idx_credit_tx_type    ON credit_transactions(type);

-- Invoices (cobranças de recarga). Mesmo modelo de pagamento de orders.
CREATE TABLE IF NOT EXISTS invoices (
    id                  TEXT PRIMARY KEY,
    user_id             TEXT NOT NULL REFERENCES users(id),
    amount_cents        BIGINT NOT NULL,               -- BRL cents (base de contabilidade)
    currency            TEXT NOT NULL DEFAULT 'BRL',
    display_currency    TEXT NOT NULL DEFAULT 'BRL',
    display_amount      TEXT NOT NULL DEFAULT '',
    settlement_currency TEXT NOT NULL DEFAULT 'BRL',
    settlement_amount   TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'pending', -- pending | paid | failed | cancelled
    gateway_id          TEXT REFERENCES payment_gateways(id),
    external_ref        TEXT,
    payment_url         TEXT,
    payment_extra       JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    paid_at             TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_invoices_user   ON invoices(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_invoices_status ON invoices(status);
