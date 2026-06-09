-- Email reputation tracking — alimentado por webhook do Resend.
--
-- Cada evento (delivered/bounced/complained/opened/clicked) entra como linha
-- em email_events; email_reputation é o sumário rolante por endereço.
--
-- Política de auto-disable:
--   - 1 hard bounce: marca disabled=true imediatamente. Resend não aceita
--     retentar endereço que retornou 5xx (suppression list já cuida no
--     lado deles, mas armazenamos pra UI e relatórios).
--   - 1 complaint (mark-as-spam): disabled=true. Recuperação manual no
--     backoffice ("opt-in re-confirmation").
--   - Soft bounces NÃO derrubam — Resend retenta automaticamente.

BEGIN;

CREATE TABLE IF NOT EXISTS email_events (
    id           TEXT PRIMARY KEY,
    email        TEXT NOT NULL,
    event_type   TEXT NOT NULL,    -- delivered | bounced | complained | opened | clicked | hard_bounce
    bounce_type  TEXT,             -- 'hard' | 'soft' | null (só pra event_type=bounced)
    payload      JSONB NOT NULL,   -- snapshot do webhook completo pra debugging
    received_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_email_events_email      ON email_events(email, received_at DESC);
CREATE INDEX IF NOT EXISTS idx_email_events_type       ON email_events(event_type, received_at DESC);

CREATE TABLE IF NOT EXISTS email_reputation (
    email             TEXT PRIMARY KEY,
    disabled          BOOLEAN NOT NULL DEFAULT FALSE,
    disabled_reason   TEXT,                  -- 'hard_bounce' | 'complaint' | 'manual'
    disabled_at       TIMESTAMPTZ,
    last_event_at     TIMESTAMPTZ,
    hard_bounce_count INT NOT NULL DEFAULT 0,
    soft_bounce_count INT NOT NULL DEFAULT 0,
    complaint_count   INT NOT NULL DEFAULT 0,
    delivered_count   INT NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_email_reputation_disabled ON email_reputation(disabled) WHERE disabled = TRUE;

COMMIT;
