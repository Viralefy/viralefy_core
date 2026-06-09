-- Reverte 011: volta rates pra base BRL.

BEGIN;
UPDATE currencies SET rate = 1.0          WHERE code = 'BRL';
UPDATE currencies SET rate = 0.185        WHERE code = 'USD';
UPDATE currencies SET rate = 0.185        WHERE code = 'USDT';
UPDATE currencies SET rate = 0.17         WHERE code = 'EUR';
UPDATE currencies SET rate = 0.0000019    WHERE code = 'BTC';
COMMIT;
