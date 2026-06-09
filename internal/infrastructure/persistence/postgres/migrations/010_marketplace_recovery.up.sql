-- Marketplace + Account Recovery LP infra.
--
-- 4 categorias novas:
--   - recuperacao_perfil   (LP própria por país com formulário; preço $10k)
--   - bms_facebook         (Business Manager FB pré-existentes)
--   - perfis_redes         (perfis com seguidores reais)
--   - emails_validados     (pacotes de e-mails validados)
--
-- Pra carregar dados de formulário (recovery: data de banimento, motivo,
-- última publicação; BMs/perfis: dados de entrega), adicionamos
-- `orders.custom_data jsonb`. E pra ligar order → ticket aberto no
-- pagamento, `orders.ticket_id` referencia tickets.
--
-- Tudo idempotente (IF NOT EXISTS / ON CONFLICT).

BEGIN;

-- ---- 1. Categorias novas no catálogo --------------------------------------
INSERT INTO categories (code, label, sort_order, active) VALUES
  ('recuperacao_perfil', 'Account recovery', 12, true),
  ('bms_facebook',       'Facebook Business Managers', 13, true),
  ('perfis_redes',       'Aged social profiles', 14, true),
  ('emails_validados',   'Validated email packs', 15, true)
ON CONFLICT (code) DO UPDATE SET
  label = EXCLUDED.label,
  sort_order = EXCLUDED.sort_order,
  active = EXCLUDED.active;

-- ---- 2. orders.custom_data + orders.ticket_id -----------------------------
-- custom_data carrega o snapshot do formulário (recovery, marketplace).
-- Schema livre por order — backend valida no use case.
ALTER TABLE orders ADD COLUMN IF NOT EXISTS custom_data JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS ticket_id   TEXT REFERENCES tickets(id);
CREATE INDEX IF NOT EXISTS idx_orders_ticket ON orders(ticket_id) WHERE ticket_id IS NOT NULL;

-- ---- 3. Move "Account recovery" de servicos para a nova categoria ---------
-- O plano "Account recovery" estava em 'servicos' a $199. Migra pra
-- recuperacao_perfil e ajusta preço pra $10,000 (canônico USD).
-- seed.go também passa a emitir já dessa forma.
UPDATE plans SET category = 'recuperacao_perfil', price_cents = 1000000
 WHERE category = 'servicos' AND name = 'Account recovery';

-- E refresca os plan_prices em todas as moedas conforme rates inline do seed
-- (USD=1, USDT=1, EUR=0.92, BRL=5.41, BTC=0.0000103). Apenas se o plano
-- existir.
DO $$
DECLARE plan_uuid TEXT;
BEGIN
  SELECT id INTO plan_uuid FROM plans WHERE category = 'recuperacao_perfil' AND name = 'Account recovery' LIMIT 1;
  IF plan_uuid IS NOT NULL THEN
    INSERT INTO plan_prices (plan_id, currency_code, amount) VALUES
      (plan_uuid, 'USD',   '10000.00'),
      (plan_uuid, 'USDT',  '10000.00'),
      (plan_uuid, 'EUR',   '9200.00'),
      (plan_uuid, 'BRL',   '54100.00'),
      (plan_uuid, 'BTC',   '0.10300000')
    ON CONFLICT (plan_id, currency_code) DO UPDATE SET amount = EXCLUDED.amount;
  END IF;
END $$;

COMMIT;
