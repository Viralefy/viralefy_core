-- A/B testing harness (Fase 6.6).
--
-- 3 tabelas:
--   ab_experiments — definição: chave, descrição, pesos por variant (JSONB).
--   ab_assignments — sticky: visitor_id+experiment_key → variant. Garantia
--     de que o mesmo visitante sempre vê a mesma variant entre requests.
--   ab_events      — exposure/conversion/custom. Append-only. Index por
--     experiment_key+occurred_at pra ranking de funnels.
--
-- Visitor ID é gerado no front (UUID em cookie/localStorage 1y). Service
-- de assignment usa hash determinístico (visitor_id+experiment_key) pra
-- decidir variant — reproduzível mesmo se a row de assignment for perdida.

BEGIN;

CREATE TABLE IF NOT EXISTS ab_experiments (
    key         TEXT PRIMARY KEY,
    description TEXT,
    variants    JSONB NOT NULL,
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS ab_assignments (
    visitor_id     TEXT NOT NULL,
    experiment_key TEXT NOT NULL REFERENCES ab_experiments(key),
    variant        TEXT NOT NULL,
    assigned_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (visitor_id, experiment_key)
);

CREATE TABLE IF NOT EXISTS ab_events (
    id             TEXT PRIMARY KEY,
    visitor_id     TEXT NOT NULL,
    experiment_key TEXT NOT NULL,
    variant        TEXT NOT NULL,
    event_name     TEXT NOT NULL,
    payload        JSONB,
    occurred_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ab_events_exp ON ab_events(experiment_key, occurred_at DESC);

-- Seed: experimento de exemplo (inativo, pra validar schema no boot).
INSERT INTO ab_experiments (key, description, variants, active) VALUES
    ('homepage_hero_v1', 'Test new hero copy variants', '{"control":50,"variant_a":50}'::jsonb, FALSE)
ON CONFLICT (key) DO NOTHING;

COMMIT;
