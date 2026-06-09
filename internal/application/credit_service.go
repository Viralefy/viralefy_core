package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type CreditService struct {
	repo domain.CreditRepository
}

func NewCreditService(repo domain.CreditRepository) *CreditService {
	return &CreditService{repo: repo}
}

func (s *CreditService) Balance(ctx context.Context, userID string) (*domain.CreditAccount, error) {
	return s.repo.GetOrCreateAccount(ctx, userID)
}

func (s *CreditService) History(ctx context.Context, userID string, limit int) ([]domain.CreditTransaction, error) {
	return s.repo.ListByUser(ctx, userID, limit)
}

// Spend reduz o saldo (saída). orderID identifica o pedido pago.
func (s *CreditService) Spend(ctx context.Context, userID string, cents int64, description string, orderID *string) (*domain.CreditAccount, error) {
	if cents <= 0 {
		return nil, domain.ErrInvalidInput
	}
	return s.repo.Apply(ctx, domain.CreditTransaction{
		ID:          uuid.New().String(),
		UserID:      userID,
		Type:        domain.CreditTxSpend,
		AmountCents: -cents, // saída
		Currency:    "USD",
		OrderID:     orderID,
		Description: description,
	})
}

// Recharge credita saldo (entrada). invoiceID identifica a invoice paga.
func (s *CreditService) Recharge(ctx context.Context, userID string, cents int64, invoiceID *string, description string) (*domain.CreditAccount, error) {
	if cents <= 0 {
		return nil, domain.ErrInvalidInput
	}
	return s.repo.Apply(ctx, domain.CreditTransaction{
		ID:          uuid.New().String(),
		UserID:      userID,
		Type:        domain.CreditTxRecharge,
		AmountCents: cents, // entrada
		Currency:    "USD",
		InvoiceID:   invoiceID,
		Description: description,
	})
}

// AdminAdjustment cria um ajuste manual (entrada ou saída). delta com sinal.
func (s *CreditService) AdminAdjustment(ctx context.Context, userID string, delta int64, description string) (*domain.CreditAccount, error) {
	if delta == 0 {
		return nil, domain.ErrInvalidInput
	}
	return s.repo.Apply(ctx, domain.CreditTransaction{
		ID:          uuid.New().String(),
		UserID:      userID,
		Type:        domain.CreditTxAdjustment,
		AmountCents: delta,
		Currency:    "USD",
		Description: description,
	})
}
