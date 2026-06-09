-- Purchasing Power Parity multipliers por país (Fase 6.5).
--
-- Design: USD canonical NUNCA muda. Settlement intocado. O multiplier afeta
-- exclusivamente o display_amount que o front mostra (front aplica via
-- priceForCountry()). Geo-detect → countryCode → multiplier → display ajustado.
--
-- Bracket: 0.10 a 1.00. Países T1 (US/CA/EU/AU) ficam ~1.00. Emergentes T2
-- (BR/MX/AR/CO/CL/PE) 0.50-0.70. T3 (IN/PH/ID/VN/TH/EG/NG/KE) 0.30-0.55.
-- Valores baseados em PPP do FMI 2024 com floor de 0.30 pra preservar margem.

BEGIN;

CREATE TABLE IF NOT EXISTS country_ppp (
    country_code TEXT PRIMARY KEY,
    multiplier   NUMERIC(4,2) NOT NULL DEFAULT 1.00 CHECK (multiplier BETWEEN 0.10 AND 1.00),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO country_ppp (country_code, multiplier) VALUES
    ('br', 0.55), ('mx', 0.65), ('ar', 0.50), ('co', 0.55), ('cl', 0.70), ('pe', 0.55),
    ('in', 0.35), ('ph', 0.40), ('id', 0.40), ('vn', 0.40), ('th', 0.55), ('eg', 0.35),
    ('ng', 0.30), ('ke', 0.35), ('tr', 0.45), ('ua', 0.40), ('za', 0.50),
    ('us', 1.00), ('ca', 0.95), ('gb', 0.90), ('de', 0.90), ('fr', 0.90), ('it', 0.85),
    ('es', 0.85), ('au', 0.95), ('nl', 0.90), ('se', 0.90), ('pl', 0.65)
ON CONFLICT (country_code) DO NOTHING;

COMMIT;
