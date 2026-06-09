-- 033_user_events — captura de comportamento do usuário (landing, pageviews,
-- clicks, abertura/fechamento de modal, checkout) pra remarketing futuro e
-- jornada agregada (landing_path/total_events) usada pelo backoffice.
--
-- Duas tabelas:
--   user_events    — append-only, granular. visitor_id é client-supplied
--                    (vf_vid em localStorage/cookie); user_id é populado só
--                    quando há sessão autenticada.
--   user_journeys  — agregado 1:1 por user. landing_* gravado APENAS na
--                    primeira gravação (first-touch wins).
BEGIN;

CREATE TABLE IF NOT EXISTS user_events (
  id          TEXT PRIMARY KEY,
  visitor_id  TEXT NOT NULL,
  user_id     TEXT REFERENCES users(id),
  event_type  TEXT NOT NULL,
  path        TEXT,
  referrer    TEXT,
  payload     JSONB,
  utm         JSONB,
  ip          TEXT,
  user_agent  TEXT,
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_events_visitor   ON user_events(visitor_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_events_user      ON user_events(user_id, occurred_at DESC) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_user_events_type_time ON user_events(event_type, occurred_at DESC);

CREATE TABLE IF NOT EXISTS user_journeys (
  user_id          TEXT PRIMARY KEY REFERENCES users(id),
  landing_path     TEXT,
  landing_referrer TEXT,
  landing_utm      JSONB,
  first_seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  total_events     INT NOT NULL DEFAULT 0,
  total_orders     INT NOT NULL DEFAULT 0
);

COMMIT;
