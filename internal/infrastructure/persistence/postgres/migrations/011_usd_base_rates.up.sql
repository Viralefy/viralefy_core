-- Switch currency rate base from BRL to USD.
--
-- Antes: `rate` = unidades da moeda por 1 BRL  (BRL=1, USD=0.185, EUR=0.17).
-- Agora: `rate` = unidades da moeda por 1 USD  (USD=1, USDT=1, EUR=0.92,
--                                                BRL=5.41, BTC=0.0000103).
--
-- USD vira a moeda base canônica do sistema. BRL passa a ser subsidiária.
-- Os rates batem com os inline rates do seedPlanPrices, então plan_prices
-- já está coerente — só falta atualizar `currencies.rate` pra refletir.
--
-- Idempotente.

BEGIN;

UPDATE currencies SET rate = 1.0          WHERE code = 'USD';
UPDATE currencies SET rate = 1.0          WHERE code = 'USDT';
UPDATE currencies SET rate = 0.92         WHERE code = 'EUR';
UPDATE currencies SET rate = 5.41         WHERE code = 'BRL';
UPDATE currencies SET rate = 0.0000103    WHERE code = 'BTC';

COMMIT;
