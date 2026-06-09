-- Tax rates EU VAT (Fase 5.3).
--
-- Design: tabela canônica country_code → rate_pct (NUMERIC(5,2)) + rate_type.
-- O front baixa o catálogo via /v1/tax-rates uma vez por sessão e pre-computa
-- o VAT no display do checkout. O cálculo autoritativo roda server-side em
-- application.TaxService.ComputeTax; orders.tax_usd_cents grava o valor
-- final cobrado (amount_charged = amount_cents + tax_usd_cents). Settlement
-- continua em USDT — tax é repassado integralmente no campo tax_usd_cents
-- pra audit e geração de invoice/breakdown.
--
-- Países cobertos: 27 estados-membros UE + GB (post-Brexit, mantemos VAT por
-- compliance histórica e por causa de NI). Rates de 2026; revisar a cada
-- ciclo fiscal. ON CONFLICT DO NOTHING mantém edits manuais via SQL.

BEGIN;

CREATE TABLE IF NOT EXISTS tax_rates (
    country_code TEXT PRIMARY KEY,
    rate_pct     NUMERIC(5,2) NOT NULL DEFAULT 0,
    rate_type    TEXT NOT NULL DEFAULT 'VAT',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO tax_rates (country_code, rate_pct, rate_type) VALUES
    ('at',20.00,'VAT'),('be',21.00,'VAT'),('bg',20.00,'VAT'),('cy',19.00,'VAT'),
    ('cz',21.00,'VAT'),('de',19.00,'VAT'),('dk',25.00,'VAT'),('ee',22.00,'VAT'),
    ('es',21.00,'VAT'),('fi',25.50,'VAT'),('fr',20.00,'VAT'),('gr',24.00,'VAT'),
    ('hr',25.00,'VAT'),('hu',27.00,'VAT'),('ie',23.00,'VAT'),('it',22.00,'VAT'),
    ('lt',21.00,'VAT'),('lu',17.00,'VAT'),('lv',21.00,'VAT'),('mt',18.00,'VAT'),
    ('nl',21.00,'VAT'),('pl',23.00,'VAT'),('pt',23.00,'VAT'),('ro',19.00,'VAT'),
    ('se',25.00,'VAT'),('si',22.00,'VAT'),('sk',20.00,'VAT'),
    ('gb',20.00,'VAT')
ON CONFLICT (country_code) DO NOTHING;

ALTER TABLE orders ADD COLUMN IF NOT EXISTS tax_country_code TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS tax_rate_pct NUMERIC(5,2);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS tax_usd_cents INT NOT NULL DEFAULT 0;

COMMIT;
