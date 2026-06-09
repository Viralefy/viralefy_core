-- Reverte 009: USDT volta para invisível com símbolo ₮ e BRL como #1.

UPDATE currencies SET display_enabled = false, symbol = '₮', sort_order = 5
 WHERE code = 'USDT';

UPDATE currencies SET sort_order = 1 WHERE code = 'BRL';
UPDATE currencies SET sort_order = 2 WHERE code = 'USD';
UPDATE currencies SET sort_order = 3 WHERE code = 'EUR';
UPDATE currencies SET sort_order = 4 WHERE code = 'BTC';
