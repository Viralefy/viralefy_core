-- Referral system (Fase 6.4).
--
-- Cada user tem um referral_code único (gerado on-demand pelo
-- ReferralService.GetOrCreateCode). Visitante usando ?ref=<code> ganha
-- sticky cookie no front (30 dias). No primeiro pagamento confirmado do
-- novo user, o referrer ganha 5% do amount em créditos (USD-cents).
--
-- referred_by_user_id é populado UMA VEZ na criação do user (quando
-- tracking.referrer_code está presente); imutável depois. O referral_rewards
-- guarda o audit row de cada recompensa concedida (UNIQUE por order_id pra
-- garantir idempotência).

BEGIN;

ALTER TABLE users ADD COLUMN IF NOT EXISTS referral_code TEXT UNIQUE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS referred_by_user_id TEXT REFERENCES users(id);

CREATE INDEX IF NOT EXISTS idx_users_referred_by ON users(referred_by_user_id);

CREATE TABLE IF NOT EXISTS referral_rewards (
    id                TEXT PRIMARY KEY,
    referrer_user_id  TEXT NOT NULL REFERENCES users(id),
    referred_user_id  TEXT NOT NULL REFERENCES users(id),
    order_id          TEXT NOT NULL UNIQUE REFERENCES orders(id),
    reward_usd_cents  INT NOT NULL CHECK (reward_usd_cents > 0),
    granted_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_referral_rewards_referrer ON referral_rewards(referrer_user_id);
CREATE INDEX IF NOT EXISTS idx_referral_rewards_referred ON referral_rewards(referred_user_id);

COMMIT;
