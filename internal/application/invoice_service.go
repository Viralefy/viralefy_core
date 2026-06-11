package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
)

// InvoiceService orquestra cobranças de recarga de créditos. Quando paga,
// dispara CreditService.Recharge para creditar o saldo do usuário.
type InvoiceService struct {
	invoices   domain.InvoiceRepository
	gateways   domain.GatewayRepository
	users      domain.UserRepository
	credits    *CreditService
	currencies *CurrencyService
	payments   *PaymentRegistry
}

func NewInvoiceService(
	invoices domain.InvoiceRepository,
	gateways domain.GatewayRepository,
	users domain.UserRepository,
	credits *CreditService,
	currencies *CurrencyService,
	payments *PaymentRegistry,
) *InvoiceService {
	return &InvoiceService{
		invoices: invoices, gateways: gateways, users: users, credits: credits,
		currencies: currencies, payments: payments,
	}
}

type CreateInvoiceInput struct {
	UserID          string
	AmountCents     int64  // canônico em USD-cents (vira saldo USD no ledger)
	DisplayCurrency string // moeda escolhida para exibição/cobrança (BRL, EUR, USDT…)
}

// Create gera invoice + tenta criar charge no gateway adequado.
func (s *InvoiceService) Create(ctx context.Context, in CreateInvoiceInput) (*domain.Invoice, error) {
	if in.AmountCents <= 0 || in.UserID == "" {
		return nil, domain.ErrInvalidInput
	}
	// Mínimo razoável: USD 5.00 pra evitar lixo nos gateways (PIX, Heleket etc.).
	if in.AmountCents < 500 {
		return nil, domain.ErrInvalidInput
	}
	u, err := s.users.GetByID(ctx, in.UserID)
	if err != nil {
		return nil, err
	}
	quote, err := s.currencies.QuoteForPlan(ctx, nil, int(in.AmountCents), in.DisplayCurrency)
	if err != nil {
		return nil, err
	}

	gw := s.pickGateway(ctx, quote.SettlementCurrency)
	var gwID *string
	if gw != nil {
		gwID = &gw.ID
	}

	inv := domain.Invoice{
		ID:                 uuid.New().String(),
		UserID:             in.UserID,
		AmountCents:        in.AmountCents,
		// Currency é a moeda canônica do invoice (= moeda base). USD ✓.
		Currency:           "USD",
		DisplayCurrency:    quote.DisplayCurrency,
		DisplayAmount:      quote.DisplayAmount,
		SettlementCurrency: quote.SettlementCurrency,
		SettlementAmount:   quote.SettlementAmount,
		Status:             domain.InvoiceStatusPending,
		GatewayID:          gwID,
	}
	if err := s.invoices.Create(ctx, inv); err != nil {
		return nil, err
	}

	if gw != nil {
		if p, ok := s.payments.Get(gw.Provider); ok {
			charge, perr := p.CreateCharge(ctx, PaymentChargeInput{
				OrderID:     inv.ID,
				GatewayID:   gw.ID,
				Provider:    gw.Provider,
				Description: "Viralefy credits top-up",
				Amount:      quote.SettlementAmount,
				Currency:    quote.SettlementCurrency,
				Customer:    PaymentCustomer{Name: u.Name, Email: u.Email},
				Config:      gw.Config,
			})
			if perr != nil {
				observability.FromContext(ctx).Warn("invoice: payment provider failed",
					"provider", gw.Provider,
					"error", perr.Error(),
				)
			} else {
				inv.ExternalRef = &charge.ExternalRef
				inv.PaymentURL = &charge.PaymentURL
				inv.PaymentExtra = charge.Extra
				_ = s.invoices.UpdatePayment(ctx, inv.ID, charge.ExternalRef, charge.PaymentURL, charge.Extra)
			}
		}
	}
	return &inv, nil
}

func (s *InvoiceService) pickGateway(ctx context.Context, settlement string) *domain.PaymentGateway {
	candidate := ""
	switch settlement {
	case "BRL":
		candidate = "woovi"
	case "USDT", "BTC":
		candidate = "heleket"
	}
	if candidate != "" {
		if g, err := s.gateways.GetActiveByProvider(ctx, candidate); err == nil && g != nil {
			return g
		}
	}
	g, _ := s.gateways.GetDefaultActive(ctx)
	return g
}

func (s *InvoiceService) ListForUser(ctx context.Context, userID string) ([]domain.Invoice, error) {
	return s.invoices.ListByUser(ctx, userID)
}

func (s *InvoiceService) Get(ctx context.Context, id, userID string) (*domain.Invoice, error) {
	inv, err := s.invoices.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if userID != "" && inv.UserID != userID {
		return nil, domain.ErrForbidden
	}
	return inv, nil
}

// AdminList lista invoices p/ backoffice.
// AdminGet expõe lookup direto pra handlers admin (sem checar ownership).
func (s *InvoiceService) AdminGet(ctx context.Context, id string) (*domain.Invoice, error) {
	return s.invoices.GetByID(ctx, id)
}

func (s *InvoiceService) AdminList(ctx context.Context, statusFilter string) ([]domain.Invoice, error) {
	return s.invoices.ListAll(ctx, statusFilter)
}

// AdminListView devolve invoices + user hidratado (nome/email). Usado pelo
// backoffice em /admin/invoices pra renderizar o owner sem N+1.
func (s *InvoiceService) AdminListView(ctx context.Context, statusFilter string) ([]domain.InvoiceView, error) {
	return s.invoices.ListAllView(ctx, statusFilter)
}

// AdminSoftDelete / AdminHardDelete / AdminRestore — pass-through pro repo.
// Mantém a regra de quem pode chamar no nível HTTP (RequirePermission /
// RequireSuperadmin); aqui só repassamos.
func (s *InvoiceService) AdminSoftDelete(ctx context.Context, id, adminID, reason string) error {
	return s.invoices.SoftDeleteInvoice(ctx, id, adminID, reason)
}

func (s *InvoiceService) AdminHardDelete(ctx context.Context, id string) error {
	return s.invoices.HardDeleteInvoice(ctx, id)
}

func (s *InvoiceService) AdminRestore(ctx context.Context, id string) error {
	return s.invoices.RestoreInvoice(ctx, id)
}

// AdminListDeleted devolve invoices soft-deleted (com user hidratado) pra
// a aba Trash do superadmin. Pass-through pro repo.
func (s *InvoiceService) AdminListDeleted(ctx context.Context, limit int) ([]domain.InvoiceView, error) {
	return s.invoices.ListDeletedView(ctx, limit)
}

// AdminMarkPaid: marca invoice como paga e credita o saldo (idempotente).
// Útil enquanto webhook não está plugado (admin confirma manualmente).
func (s *InvoiceService) AdminMarkPaid(ctx context.Context, id string) (*domain.Invoice, error) {
	inv, err := s.invoices.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if inv.Status == domain.InvoiceStatusPaid {
		return inv, nil // já estava paga — idempotente
	}
	if err := s.invoices.MarkPaid(ctx, id); err != nil {
		return nil, err
	}
	// Credita saldo via ledger.
	if _, err := s.credits.Recharge(ctx, inv.UserID, inv.AmountCents, &inv.ID, "Top-up via invoice "+inv.ID[:8]); err != nil {
		return nil, err
	}
	return s.invoices.GetByID(ctx, id)
}
