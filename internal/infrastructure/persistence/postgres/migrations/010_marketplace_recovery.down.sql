-- Reverte 010.

BEGIN;

-- Volta Account recovery pra servicos e preço antigo.
UPDATE plans SET category = 'servicos', price_cents = 19900
 WHERE category = 'recuperacao_perfil' AND name = 'Account recovery';

UPDATE plan_prices SET amount = '199.00' WHERE currency_code = 'USD'
  AND plan_id IN (SELECT id FROM plans WHERE category = 'servicos' AND name = 'Account recovery');

-- Remove categorias (só se vazias).
DELETE FROM categories
 WHERE code IN ('recuperacao_perfil', 'bms_facebook', 'perfis_redes', 'emails_validados')
   AND NOT EXISTS (SELECT 1 FROM plans WHERE plans.category = categories.code);

DROP INDEX IF EXISTS idx_orders_ticket;
ALTER TABLE orders DROP COLUMN IF EXISTS ticket_id;
ALTER TABLE orders DROP COLUMN IF EXISTS custom_data;

COMMIT;
