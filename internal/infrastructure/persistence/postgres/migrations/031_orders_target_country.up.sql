-- target_country_code identifica o MERCADO da entrega. Importante porque
-- /us/instagram-followers e /de/instagram-followers compartilham o mesmo
-- plan_id, mas o operador precisa saber se entregou seguidor americano vs
-- alemão pra escolher o supplier correto.
--
-- Diferente de tax_country_code (país do comprador, pra VAT). Ex.:
--   user em São Paulo compra seguidores alemães → target=de, tax=br.
BEGIN;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS target_country_code TEXT;
CREATE INDEX IF NOT EXISTS idx_orders_target_country ON orders(target_country_code) WHERE target_country_code IS NOT NULL;
COMMIT;
