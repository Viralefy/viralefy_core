-- Cada gateway aceita um subconjunto das moedas globais. O checkout precisa
-- saber, dado um order em (display_currency, settlement_currency), qual
-- gateway pode efetivamente processar — sem isso, USDT bate em Woovi e
-- BRL bate em Heleket. GIN porque a busca é "gateway que tem X na lista".
BEGIN;
ALTER TABLE payment_gateways
    ADD COLUMN IF NOT EXISTS accepted_currencies TEXT[]
    DEFAULT ARRAY['USDT','USD']::TEXT[];

CREATE INDEX IF NOT EXISTS idx_gateways_active_ccy
    ON payment_gateways USING GIN (accepted_currencies)
    WHERE active = TRUE;
COMMIT;
