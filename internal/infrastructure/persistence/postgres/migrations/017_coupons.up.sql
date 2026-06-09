-- Cupons promocionais. Aplicados no checkout via CouponService.Validate.
--
-- Tipos suportados:
--   - percent: discount_value=10 → 10% off
--   - fixed_usd_cents: discount_value=500 → $5 off (em USD canonical)
--
-- Restrições opcionais (null = sem restrição):
--   - max_uses + used_count: bucket global de usos
--   - valid_from/valid_until: janela temporal
--   - min_order_usd_cents: piso de ticket
--   - only_categories: array de CategoryCode
--   - only_first_order: marca only-novo-cliente
--
-- coupon_redemptions é audit log + garantia 1-coupon-por-order via UNIQUE.

BEGIN;

CREATE TABLE IF NOT EXISTS coupons (
    code                TEXT PRIMARY KEY,                   -- "BLACK10" — case-sensitive, mas service normaliza UPPER
    description         TEXT NOT NULL DEFAULT '',
    discount_type       TEXT NOT NULL CHECK (discount_type IN ('percent', 'fixed_usd_cents')),
    discount_value      INT  NOT NULL CHECK (discount_value > 0),
    max_uses            INT  CHECK (max_uses IS NULL OR max_uses > 0),
    used_count          INT  NOT NULL DEFAULT 0 CHECK (used_count >= 0),
    valid_from          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_until         TIMESTAMPTZ,
    min_order_usd_cents INT,
    only_categories     TEXT[],
    only_first_order    BOOLEAN NOT NULL DEFAULT FALSE,
    active              BOOLEAN NOT NULL DEFAULT TRUE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (
        (discount_type = 'percent' AND discount_value BETWEEN 1 AND 100) OR
        (discount_type = 'fixed_usd_cents' AND discount_value >= 1)
    )
);

CREATE INDEX IF NOT EXISTS idx_coupons_active ON coupons(active) WHERE active = TRUE;

CREATE TABLE IF NOT EXISTS coupon_redemptions (
    id                 TEXT PRIMARY KEY,
    coupon_code        TEXT NOT NULL REFERENCES coupons(code),
    order_id           TEXT NOT NULL UNIQUE REFERENCES orders(id),
    user_email         TEXT NOT NULL,
    discount_usd_cents INT  NOT NULL CHECK (discount_usd_cents > 0),
    redeemed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_coupon_redemptions_coupon ON coupon_redemptions(coupon_code);
CREATE INDEX IF NOT EXISTS idx_coupon_redemptions_email  ON coupon_redemptions(user_email);

-- Superadmin bypassa Can() (ver domain/authz.go); permissions adicionais
-- pra outros papéis ficam pra quando o segundo papel existir.

COMMIT;
