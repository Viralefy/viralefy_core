-- 041_user_consent — LGPD Art. 8 §6 (comprovação do consentimento) +
-- ajuste de privacy-by-default em user_events.
--
-- Contexto: o cookie banner default-on pra analytics violava Art. 8 §3
-- (consent não-livre). A correção do front (default OFF + opt-in) é
-- necessária mas não suficiente: o backend precisa
--   (a) gravar histórico imutável das decisões de consent,
--   (b) parar de capturar IP/UA em user_events quando NÃO há consent.
--
-- Duas estruturas:
--
--   user_consent_log    — append-only. Cada clique em "Aceitar todos" /
--                          "Apenas essenciais" / "Salvar personalizado" vira
--                          1 row. Imutável (sem UPDATE/DELETE no app code).
--                          IP+UA aqui são gravados SEMPRE — a base legal pra
--                          isso é a própria comprovação do consent (Art. 7 II
--                          obrigação legal + Art. 8 §6). É legítimo.
--
--   user_events         — adiciona coluna `analytics_consent BOOLEAN` pra
--                          saber a posteriori se a row gravou PII ou não.
--                          Backend agora SETA ip=NULL, user_agent=NULL quando
--                          o header `X-Analytics-Consent` não for "1". A
--                          coluna serve pra (a) auditar e (b) backfill —
--                          podemos NULLificar IP/UA de rows antigas onde
--                          analytics_consent=false sem perder o flag de
--                          consent original.
BEGIN;

-- user_consent_log: append-only, particionável por mês no futuro se crescer.
-- Sem FK pra users pq visitantes anônimos também consentem (user_id NULL).
CREATE TABLE IF NOT EXISTS user_consent_log (
  id              TEXT PRIMARY KEY,
  user_id         TEXT REFERENCES users(id),
  visitor_id      TEXT,
  version         INT  NOT NULL,
  necessary       BOOLEAN NOT NULL DEFAULT TRUE,
  preferences     BOOLEAN NOT NULL,
  analytics       BOOLEAN NOT NULL,
  marketing       BOOLEAN NOT NULL,
  source          TEXT NOT NULL,   -- 'accept_all' | 'essential_only' | 'custom' | 'reset'
  ip              TEXT,            -- legítimo aqui: comprovação Art. 8 §6
  user_agent      TEXT,            -- idem
  recorded_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_consent_log_user
  ON user_consent_log(user_id, recorded_at DESC)
  WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_user_consent_log_visitor
  ON user_consent_log(visitor_id, recorded_at DESC);

-- user_events: nullable boolean. NULL = pré-migration / desconhecido (legacy
-- rows que existiam antes do gate). FALSE = consent negado, IP/UA não
-- gravados. TRUE = consent dado, IP/UA gravados normalmente.
ALTER TABLE user_events
  ADD COLUMN IF NOT EXISTS analytics_consent BOOLEAN;

-- Backfill defensivo das rows legadas: assumimos que tinham consent
-- (default-on antigo do banner) — mas NÃO mexemos no IP/UA das rows antigas
-- pra preservar histórico. Operação de NULLify retroativa fica pra runbook
-- de "honrar revogação retroativa" (Art. 18 IX).
UPDATE user_events
   SET analytics_consent = TRUE
 WHERE analytics_consent IS NULL
   AND occurred_at < NOW();

COMMIT;
