package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
)

// subscriptionCycleDays — todo mês ~30 dias. Não tentamos calendar
// arithmetic exato (28/29/30/31) porque o MVP tolera drift de ±1d e o
// cron tickando 1h corrige naturalmente: se next_billing_at cair em
// 30d e o user pagar em 31d, próximo ciclo já vira 31d-base + 30d.
const subscriptionCycleDays = 30

// SubscriptionService encapsula assinaturas recorrentes. Nunca toca em
// CheckoutService internals — usa só Checkout() público pra gerar a order
// + payment_url do ciclo. PaymentReceiver continua confirmando normal
// quando o user pagar; o vínculo é via subscription_id na order.
type SubscriptionService struct {
	repo     domain.SubscriptionRepository
	checkout *CheckoutService
	users    domain.UserRepository
	plans    domain.PlanRepository
	profiles domain.ProfileRepository
}

// NewSubscriptionService construtor. CheckoutService é obrigatório porque
// é o ponto de entrada pro fluxo de pagamento em cada renovação.
func NewSubscriptionService(
	repo domain.SubscriptionRepository,
	checkout *CheckoutService,
) *SubscriptionService {
	return &SubscriptionService{repo: repo, checkout: checkout}
}

// SetUsers/SetPlans/SetProfiles são opt-in: o cron precisa deles pra
// montar o CheckoutInput de renovação, mas Subscribe/Cancel podem rodar
// sem (usados em testes que não disparam o cron). Mantemos opt-in pra
// não exigir refactor de wiring em todos os testes que ainda não conhecem
// subs.
func (s *SubscriptionService) SetUsers(u domain.UserRepository)         { s.users = u }
func (s *SubscriptionService) SetPlans(p domain.PlanRepository)         { s.plans = p }
func (s *SubscriptionService) SetProfiles(p domain.ProfileRepository)   { s.profiles = p }

// Subscribe cria uma sub ativa. Idempotente: se já existir uma ATIVA pro
// mesmo user+plan, devolve a existente sem erro (não cria duplicata).
//
// next_billing_at = NOW() + 30d — o primeiro pagamento NÃO é gerado aqui
// (o user deve fazer o primeiro checkout manualmente). A sub serve como
// promessa de renovação contínua.
func (s *SubscriptionService) Subscribe(ctx context.Context, userID, planID string) (*domain.Subscription, error) {
	if userID == "" {
		return nil, domain.ErrUnauthorized
	}
	if planID == "" {
		return nil, domain.ErrInvalidInput
	}
	// Idempotência: procura uma ativa existente.
	existing, err := s.repo.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range existing {
		if existing[i].PlanID == planID && existing[i].Status == domain.SubscriptionStatusActive {
			return &existing[i], nil
		}
	}
	sub := domain.Subscription{
		ID:             uuid.New().String(),
		UserID:         userID,
		PlanID:         planID,
		Status:         domain.SubscriptionStatusActive,
		Interval:       "month",
		NextBillingAt:  time.Now().Add(subscriptionCycleDays * 24 * time.Hour),
		FailedPayments: 0,
	}
	if err := s.repo.Create(ctx, sub); err != nil {
		return nil, err
	}
	return &sub, nil
}

// Cancel termina a sub. Valida ownership — service NÃO confia no caller
// porque Cancel é exposto via handler HTTP autenticado (qualquer user
// autenticado poderia tentar cancelar a sub de outro).
func (s *SubscriptionService) Cancel(ctx context.Context, subID, userID string) error {
	if subID == "" || userID == "" {
		return domain.ErrInvalidInput
	}
	sub, err := s.repo.GetByID(ctx, subID)
	if err != nil {
		return err
	}
	if sub.UserID != userID {
		// Não vazamos existência — 401 igual sub inexistente.
		return domain.ErrUnauthorized
	}
	return s.repo.Cancel(ctx, subID)
}

// ListByUser devolve subs do user pro painel /account/subscriptions.
func (s *SubscriptionService) ListByUser(ctx context.Context, userID string) ([]domain.Subscription, error) {
	if userID == "" {
		return nil, domain.ErrUnauthorized
	}
	return s.repo.ListByUser(ctx, userID)
}

// ProcessDueRenewals é o ponto de entrada do cron. Para cada sub vencida:
//   1) tenta gerar order via CheckoutService.Checkout (tracking marca
//      auto_subscription=true pra rastreamento);
//   2) sucesso → next_billing_at += 30d, failed_payments reseta;
//   3) falha → failed_payments++; se >= threshold → cancelled.
//
// Erros são best-effort por sub (uma sub ruim não derruba o batch).
// O resultado individual fica no log estruturado.
func (s *SubscriptionService) ProcessDueRenewals(ctx context.Context) error {
	logger := observability.FromContext(ctx).With("svc", "subscriptions")
	now := time.Now()
	due, err := s.repo.ListDue(ctx, now)
	if err != nil {
		return err
	}
	if len(due) == 0 {
		return nil
	}
	logger.Info("processing due renewals", "count", len(due))
	for i := range due {
		if err := s.renewOne(ctx, &due[i]); err != nil {
			logger.Warn("renewal failed",
				"subscription_id", due[i].ID,
				"user_id", due[i].UserID,
				"error", err.Error(),
			)
		}
	}
	return nil
}

// renewOne processa uma sub. Sempre atualiza o repo (success ou fail)
// pra que a próxima iteração do cron não pegue a mesma sub no mesmo
// tick (idempotência fraca via avanço de next_billing_at).
func (s *SubscriptionService) renewOne(ctx context.Context, sub *domain.Subscription) error {
	if s.users == nil || s.plans == nil {
		return errors.New("subscription service not wired (users/plans missing)")
	}
	user, err := s.users.GetByID(ctx, sub.UserID)
	if err != nil {
		// User sumiu — cancela a sub pra não retentar pra sempre.
		_ = s.repo.Cancel(ctx, sub.ID)
		return err
	}
	plan, err := s.plans.GetByID(ctx, sub.PlanID)
	if err != nil || plan == nil || !plan.Active {
		// Plano sumiu/desativou — cancela.
		_ = s.repo.Cancel(ctx, sub.ID)
		if err == nil {
			err = errors.New("plan inactive")
		}
		return err
	}

	in := CheckoutInput{
		PlanID:          plan.ID,
		Email:           user.Email,
		Name:            user.Name,
		DisplayCurrency: plan.Currency,
		PaymentMethod:   "gateway",
		UserID:          user.ID,
		Tracking: map[string]any{
			"auto_subscription": true,
			"subscription_id":   sub.ID,
		},
	}
	// Resolve o target: pra plans com target_type=profile, pega o primeiro
	// perfil do user na plataforma certa. Pra publication, não dá pra
	// adivinhar URL — falha (será cancelada após N tentativas, como
	// qualquer falha).
	if (plan.TargetType == "" || plan.TargetType == "profile") && s.profiles != nil {
		profs, perr := s.profiles.ListByUser(ctx, user.ID)
		if perr == nil {
			for i := range profs {
				if plan.Platform == "" || string(profs[i].Platform) == plan.Platform {
					in.ProfileID = profs[i].ID
					break
				}
			}
		}
	}

	_, cerr := s.checkout.Checkout(ctx, in)
	if cerr != nil {
		// Falha → bump contador + persiste; threshold → cancelled.
		sub.FailedPayments++
		if sub.FailedPayments >= domain.SubscriptionMaxFailedPayments {
			sub.Status = domain.SubscriptionStatusCancelled
			now := time.Now()
			sub.CancelledAt = &now
		} else {
			// Empurra a próxima tentativa pra 1 dia adiante pra não martelar.
			sub.NextBillingAt = time.Now().Add(24 * time.Hour)
		}
		if uerr := s.repo.Update(ctx, *sub); uerr != nil {
			return uerr
		}
		return cerr
	}

	// Sucesso (order pending criada — user paga depois). Avança 30d e
	// reseta contador de falhas.
	sub.NextBillingAt = time.Now().Add(subscriptionCycleDays * 24 * time.Hour)
	sub.FailedPayments = 0
	return s.repo.Update(ctx, *sub)
}
