-- USDT vira a moeda de display PADRÃO da storefront. Antes ficava oculta
-- (display_enabled=false) e o front caía em USD/BRL. Agora:
--   1. USDT visível com símbolo "$" (familiar a maioria do mundo; USDT é 1:1
--      com USD, então mostrar como $ é fiel ao valor real).
--   2. sort_order=1 (primeira no seletor).
--   3. USD passa a sort_order=2, EUR=3, BRL=4, BTC=5.
--
-- Idempotente: UPSERTs por code.

UPDATE currencies SET display_enabled = true, symbol = '$', sort_order = 1
 WHERE code = 'USDT';

UPDATE currencies SET sort_order = 2 WHERE code = 'USD';
UPDATE currencies SET sort_order = 3 WHERE code = 'EUR';
UPDATE currencies SET sort_order = 4 WHERE code = 'BRL';
UPDATE currencies SET sort_order = 5 WHERE code = 'BTC';
