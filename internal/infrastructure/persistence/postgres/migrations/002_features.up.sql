-- Categorias de serviço (seguidores, engajamento, etc.)
CREATE TABLE IF NOT EXISTS categories (
    code TEXT PRIMARY KEY,
    label TEXT NOT NULL,
    sort_order INT NOT NULL DEFAULT 0,
    active BOOLEAN NOT NULL DEFAULT true
);

-- Moedas suportadas. rate = unidades desta moeda por 1 BRL (base).
-- settlement_code = moeda efetivamente cobrada (ex.: USD exibe, USDT cobra).
CREATE TABLE IF NOT EXISTS currencies (
    code TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    symbol TEXT NOT NULL,
    rate DOUBLE PRECISION NOT NULL,
    decimals INT NOT NULL DEFAULT 2,
    kind TEXT NOT NULL DEFAULT 'fiat',
    display_enabled BOOLEAN NOT NULL DEFAULT true,
    settlement_code TEXT NOT NULL,
    sort_order INT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Plano ganha categoria
ALTER TABLE plans ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT 'seguidores';

-- Pedido ganha moeda de exibição e de liquidação (multimoeda)
ALTER TABLE orders ADD COLUMN IF NOT EXISTS display_currency TEXT NOT NULL DEFAULT 'BRL';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS display_amount TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS settlement_currency TEXT NOT NULL DEFAULT 'BRL';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS settlement_amount TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_plans_category ON plans(category);
