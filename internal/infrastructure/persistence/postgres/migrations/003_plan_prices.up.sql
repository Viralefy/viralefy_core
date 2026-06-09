-- Preço manual por moeda, por plano. amount é string decimal (ex.: "9.90",
-- "0.00018") para evitar imprecisão de float. Sem linha = sem preço manual
-- naquela moeda (a aplicação faz fallback).
CREATE TABLE IF NOT EXISTS plan_prices (
    plan_id TEXT NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    currency_code TEXT NOT NULL REFERENCES currencies(code),
    amount TEXT NOT NULL,
    PRIMARY KEY (plan_id, currency_code)
);

CREATE INDEX IF NOT EXISTS idx_plan_prices_plan ON plan_prices(plan_id);
