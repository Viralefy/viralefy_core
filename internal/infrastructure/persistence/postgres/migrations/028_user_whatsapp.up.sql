-- WhatsApp opt-in + transactional sender (Fase 7.3).
--
-- Três colunas em users:
--   whatsapp_number      — número em formato E.164 (+DDIDDDNUMERO).
--                           Nullable: a maioria dos usuários nunca preenche.
--   whatsapp_opt_in      — flag explícita de opt-in. Default false: nada é
--                           enviado até o usuário clicar no toggle.
--   whatsapp_verified_at — reservado pra fluxo futuro de OTP por WhatsApp.
--                           Hoje o sender (dry-run) ignora; quando integrar
--                           Meta Cloud API/Twilio, validar antes de enviar.

BEGIN;

ALTER TABLE users ADD COLUMN IF NOT EXISTS whatsapp_number       TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS whatsapp_opt_in       BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS whatsapp_verified_at  TIMESTAMPTZ;

COMMIT;
