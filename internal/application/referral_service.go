package application

import (
	"context"
	"errors"
	"strings"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// referralRewardPct — 5% do amount em USD-cents vai pro referrer no
// primeiro pagamento do referred.
const referralRewardPct = 5

// ReferralService orquestra: emissão de código, lookup público, registro
// do referral no signup e concessão da recompensa no primeiro paid order.
//
// Não modifica CheckoutService nem PaymentReceiver — esses precisam ser
// instrumentados (hook no signup + hook pós-confirm). Wave3 / main loop
// fica responsável por ligar os hooks aos serviços.
type ReferralService struct {
	repo    domain.ReferralRepository
	users   domain.UserRepository
	credits *CreditService
}

func NewReferralService(repo domain.ReferralRepository, users domain.UserRepository, credits *CreditService) *ReferralService {
	return &ReferralService{repo: repo, users: users, credits: credits}
}

// GetOrCreateCode devolve o código do user, criando on-demand.
func (s *ReferralService) GetOrCreateCode(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		return "", domain.ErrUnauthorized
	}
	return s.repo.EnsureCode(ctx, userID)
}

// LookupCode resolve um código → user. Usado pelo handler público.
func (s *ReferralService) LookupCode(ctx context.Context, code string) (*domain.User, error) {
	return s.repo.GetByUserCode(ctx, code)
}

// MyStats devolve {code, total_referred, total_earned_cents} pro
// dashboard em /account/referral.
func (s *ReferralService) MyStats(ctx context.Context, userID string) (*domain.ReferralStats, error) {
	if userID == "" {
		return nil, domain.ErrUnauthorized
	}
	code, err := s.repo.EnsureCode(ctx, userID)
	if err != nil {
		return nil, err
	}
	n, earned, err := s.repo.Stats(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &domain.ReferralStats{
		Code:             code,
		TotalReferred:    n,
		TotalEarnedCents: earned,
	}, nil
}

// PublicInfo devolve só o suficiente pro front mostrar selo
// "Convidado por X" no checkout — sem vazar email/IDs.
func (s *ReferralService) PublicInfo(ctx context.Context, code string) (*domain.ReferralInfo, error) {
	u, err := s.repo.GetByUserCode(ctx, code)
	if err != nil {
		// 404 do repo → resposta "invalid: false" silenciosa pro front.
		if errors.Is(err, domain.ErrNotFound) {
			return &domain.ReferralInfo{Valid: false}, nil
		}
		return nil, err
	}
	first := firstNameOf(u.Name)
	return &domain.ReferralInfo{Valid: true, ReferrerName: first}, nil
}

func firstNameOf(full string) string {
	full = strings.TrimSpace(full)
	if full == "" {
		return ""
	}
	if idx := strings.IndexByte(full, ' '); idx > 0 {
		return full[:idx]
	}
	return full
}

// RecordReferral é chamado pelo CheckoutService/UserAuthService logo após
// criar o user, quando tracking.referrer_code estiver presente. Faz
// lookup do referrer e seta users.referred_by_user_id (first-touch).
//
// Idempotente: SetReferredBy só atualiza quando referred_by é NULL.
func (s *ReferralService) RecordReferral(ctx context.Context, referredUserID, referrerCode string) error {
	if referredUserID == "" || referrerCode == "" {
		return nil
	}
	referrer, err := s.repo.GetByUserCode(ctx, referrerCode)
	if err != nil {
		// código inválido → silencioso, não derruba signup.
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}
	if referrer.ID == referredUserID {
		// self-referral, ignora.
		return nil
	}
	return s.repo.SetReferredBy(ctx, referredUserID, referrer.ID)
}

// GrantOnFirstPaidOrder é o hook chamado pelo PaymentReceiver quando uma
// order confirma. Verifica:
//   - se o user tem referred_by_user_id;
//   - se é a PRIMEIRA paid order do user (evita repetir prêmio);
//   - se o reward ainda não foi registrado (UNIQUE(order_id) cobre).
//
// Se tudo OK, credita 5% do amount em USD-cents na conta do referrer
// (via CreditService.AdminAdjustment — entrada positiva no ledger).
//
// Erros são best-effort: falha aqui NÃO deve quebrar o confirm do
// pagamento. Caller deve logar e seguir.
func (s *ReferralService) GrantOnFirstPaidOrder(ctx context.Context, order *domain.Order) error {
	if order == nil || order.Status != domain.OrderStatusPaid || order.UserID == "" {
		return nil
	}
	u, err := s.users.GetByID(ctx, order.UserID)
	if err != nil {
		return err
	}
	// Sem referrer: nada a fazer.
	// (Não dá pra ler referred_by_user_id direto do *domain.User atual —
	// o struct não expõe. Lemos via Stats indireto? Não — esse path roda
	// raro; é tolerável reler com query dedicada.)
	referrerID, err := s.fetchReferrerID(ctx, order.UserID)
	if err != nil {
		return err
	}
	if referrerID == "" {
		return nil
	}
	// Já é a primeira paid? Conta as outras paid orders do user (exclui a
	// atual). Se >0, esta NÃO é a primeira → skip.
	other, err := s.countPriorPaidOrders(ctx, order.UserID, order.ID)
	if err != nil {
		return err
	}
	if other > 0 {
		return nil
	}
	reward := int64(order.AmountCents) * referralRewardPct / 100
	if reward <= 0 {
		return nil
	}
	// Grava audit row (UNIQUE order_id → idempotente).
	if err := s.repo.GrantReward(ctx, domain.GrantRewardInput{
		ReferrerUserID:  referrerID,
		ReferredUserID:  order.UserID,
		OrderID:         order.ID,
		RewardUSDCents:  reward,
	}); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			// Já recompensado em call anterior — OK.
			return nil
		}
		return err
	}
	// Credita o referrer via ledger.
	if s.credits != nil {
		_, err = s.credits.AdminAdjustment(ctx, referrerID, reward,
			"Referral reward · order "+order.ID+" · referred "+u.Email)
		if err != nil {
			return err
		}
	}
	return nil
}

// fetchReferrerID + countPriorPaidOrders são helpers internos que dependem
// de um *postgres.DB; injetamos via interface estreita pra manter o service
// testável. Por simplicidade aqui usamos uma extensão da
// ReferralRepository: se o repo expõe esses métodos, usamos; senão,
// degradamos pra no-op.
type referralRepoExt interface {
	FetchReferrerID(ctx context.Context, userID string) (string, error)
	CountPriorPaidOrders(ctx context.Context, userID, excludeOrderID string) (int, error)
}

func (s *ReferralService) fetchReferrerID(ctx context.Context, userID string) (string, error) {
	if ext, ok := s.repo.(referralRepoExt); ok {
		return ext.FetchReferrerID(ctx, userID)
	}
	return "", nil
}

func (s *ReferralService) countPriorPaidOrders(ctx context.Context, userID, excludeOrderID string) (int, error) {
	if ext, ok := s.repo.(referralRepoExt); ok {
		return ext.CountPriorPaidOrders(ctx, userID, excludeOrderID)
	}
	return 0, nil
}
