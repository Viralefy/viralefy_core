package domain

import (
	"context"
	"time"
)

// ReferralReward é o audit row de uma recompensa concedida ao referrer
// no primeiro pedido pago do referred user. UNIQUE(order_id) garante que
// o mesmo pedido nunca pague o referrer duas vezes.
type ReferralReward struct {
	ID              string    `json:"id"`
	ReferrerUserID  string    `json:"referrer_user_id"`
	ReferredUserID  string    `json:"referred_user_id"`
	OrderID         string    `json:"order_id"`
	RewardUSDCents  int64     `json:"reward_usd_cents"`
	GrantedAt       time.Time `json:"granted_at"`
}

// ReferralStats é o agregado mostrado em /account/referral.
type ReferralStats struct {
	Code            string `json:"code"`
	TotalReferred   int    `json:"total_referred"`
	TotalEarnedCents int64 `json:"total_earned_cents"`
}

// ReferralInfo é a resposta pública de GET /v1/referrals/{code}/info.
// Não expõe email/IDs — apenas se o código existe e o primeiro nome do
// referrer (para front mostrar "Convidado por X" no checkout).
type ReferralInfo struct {
	Valid        bool   `json:"valid"`
	ReferrerName string `json:"referrer_name,omitempty"`
}

// GrantRewardInput parametriza a inserção no ledger + tabela
// referral_rewards. Tudo em USD-cents (canônico do sistema).
type GrantRewardInput struct {
	ReferrerUserID  string
	ReferredUserID  string
	OrderID         string
	RewardUSDCents  int64
}

// ReferralRepository é a porta de saída pra persistência de referral.
// EnsureCode é idempotente: se o user já tem código, devolve o existente.
type ReferralRepository interface {
	// EnsureCode garante que o user tem um referral_code, criando se
	// necessário. Retry interno em caso de colisão (espaço grande mas
	// não é zero). Retorna o código final.
	EnsureCode(ctx context.Context, userID string) (string, error)

	// GetByUserCode encontra o user dono do referral_code. Retorna
	// ErrNotFound se não houver match.
	GetByUserCode(ctx context.Context, code string) (*User, error)

	// SetReferredBy seta o referrer no user. NO-OP se referred_by já
	// estiver preenchido (first-touch wins). Idempotente.
	SetReferredBy(ctx context.Context, userID, referrerID string) error

	// GrantReward grava o reward row. Retorna ErrConflict (UNIQUE
	// violation em order_id) quando o pedido já foi recompensado —
	// service trata como no-op idempotente.
	GrantReward(ctx context.Context, in GrantRewardInput) error

	// Stats devolve total_referred + total_earned_cents pra exibir
	// em /account/referral.
	Stats(ctx context.Context, userID string) (totalReferred int, totalEarnedCents int64, err error)
}
