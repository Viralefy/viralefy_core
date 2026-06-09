-- Anti-fraude velocity (Fase 4.3).
--
-- Captura sinais de abuso por IP / e-mail (futuramente fingerprint).
-- O cron FraudVelocityCron varre orders/login attempts e grava sinais
-- históricos. Severidade "warn" só observa (alimenta dashboard); "block"
-- escreve em fraud_blocks com janela de bloqueio (blocked_until).
--
-- Política inicial:
--   - 3+ orders / 24h do mesmo email → warn (email_velocity)
--   - 10+ orders / 1h do mesmo IP   → block (ip_velocity)
--   - 5+ login fails / 15min        → trackeia warn (rate-limiter já barra)
--
-- detail é JSONB livre — guarda count, window, sample_ids, etc., pra
-- forense sem ter que mexer no schema toda vez que afinarmos o tuning.

BEGIN;

CREATE TABLE IF NOT EXISTS fraud_signals (
    id TEXT PRIMARY KEY,
    signal_type TEXT NOT NULL,    -- "email_velocity" | "ip_velocity" | "login_fail"
    actor TEXT NOT NULL,           -- email ou IP
    severity TEXT NOT NULL CHECK (severity IN ('warn','block')),
    detail JSONB,
    triggered_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_fraud_signals_actor ON fraud_signals(actor, triggered_at DESC);
CREATE INDEX IF NOT EXISTS idx_fraud_signals_severity ON fraud_signals(severity, triggered_at DESC);

CREATE TABLE IF NOT EXISTS fraud_blocks (
    actor TEXT PRIMARY KEY,        -- email ou IP
    block_type TEXT NOT NULL,      -- "ip" | "email"
    reason TEXT NOT NULL,
    blocked_until TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_fraud_blocks_until ON fraud_blocks(blocked_until);

COMMIT;
