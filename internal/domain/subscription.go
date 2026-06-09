package domain

import (
	"context"
	"time"
)

// SubscriptionStatus reflete o ciclo de vida da assinatura.
//   * active    — renovações disparadas pelo cron mensalmente.
//   * paused    — placeholder pra futura "pause" manual (não usado em 6.3
//                 mas o CHECK já aceita pra evitar migration extra).
//   * cancelled — terminal (manual pelo user OU auto após N falhas).
type SubscriptionStatus string

const (
	SubscriptionStatusActive    SubscriptionStatus = "active"
	SubscriptionStatusPaused    SubscriptionStatus = "paused"
	SubscriptionStatusCancelled SubscriptionStatus = "cancelled"
)

// SubscriptionMaxFailedPayments — depois desse threshold, o cron de
// renovação muda status pra cancelled automaticamente. Valor empírico:
// 3 falhas seguidas = ~3 meses sem cobrar = quase certo que o cartão/PIX
// está morto ou o user perdeu interesse.
const SubscriptionMaxFailedPayments = 3

// Subscription é o aggregate root de assinatura recorrente. Independente
// de Order — o cron gera 1 Order pending por ciclo e linka via
// orders.subscription_id pra rastreabilidade.
type Subscription struct {
	ID              string             `json:"id"`
	UserID          string             `json:"user_id"`
	PlanID          string             `json:"plan_id"`
	Status          SubscriptionStatus `json:"status"`
	Interval        string             `json:"interval"` // por enquanto "month" sempre
	NextBillingAt   time.Time          `json:"next_billing_at"`
	CancelledAt     *time.Time         `json:"cancelled_at,omitempty"`
	FailedPayments  int                `json:"failed_payments"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
}

// SubscriptionRepository é a porta de saída pra persistência.
type SubscriptionRepository interface {
	Create(ctx context.Context, s Subscription) error
	GetByID(ctx context.Context, id string) (*Subscription, error)
	ListByUser(ctx context.Context, userID string) ([]Subscription, error)
	// ListDue devolve as subscriptions active cujo next_billing_at é <=
	// `before`. Usado pelo cron de renovação — limita batch internamente
	// pra evitar lock muito grande.
	ListDue(ctx context.Context, before time.Time) ([]Subscription, error)
	Update(ctx context.Context, s Subscription) error
	// Cancel marca status='cancelled' + cancelled_at=NOW(). Idempotente.
	Cancel(ctx context.Context, id string) error
}
