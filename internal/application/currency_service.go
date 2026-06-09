package application

import (
	"context"
	"strconv"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

type CurrencyService struct {
	repo  domain.CurrencyRepository
	plans domain.PlanRepository
}

// NewCurrencyService recebe o PlanRepository pra cascatear mudança de rate
// em plan_prices. `plans` pode ser nil em testes que não exercem cascade
// (Update vira no-op nessa parte).
func NewCurrencyService(repo domain.CurrencyRepository, plans domain.PlanRepository) *CurrencyService {
	return &CurrencyService{repo: repo, plans: plans}
}

// ListDisplayable retorna as moedas que o cliente pode escolher para exibição.
func (s *CurrencyService) ListDisplayable(ctx context.Context) ([]domain.Currency, error) {
	return s.repo.ListDisplayable(ctx)
}

// ListAll retorna todas as moedas (uso admin).
func (s *CurrencyService) ListAll(ctx context.Context) ([]domain.Currency, error) {
	return s.repo.ListAll(ctx)
}

func (s *CurrencyService) Get(ctx context.Context, code string) (*domain.Currency, error) {
	return s.repo.GetByCode(ctx, code)
}

type UpdateCurrencyInput struct {
	Code           string
	Rate           float64
	DisplayEnabled bool
	SettlementCode string
}

func (s *CurrencyService) Update(ctx context.Context, in UpdateCurrencyInput) (*domain.Currency, error) {
	if in.Rate <= 0 || in.SettlementCode == "" {
		return nil, domain.ErrInvalidInput
	}
	if _, err := s.repo.GetByCode(ctx, in.SettlementCode); err != nil {
		return nil, domain.ErrInvalidInput
	}
	if err := s.repo.UpdateRate(ctx, in.Code, in.Rate, in.DisplayEnabled, in.SettlementCode); err != nil {
		return nil, err
	}
	updated, err := s.repo.GetByCode(ctx, in.Code)
	if err != nil {
		return nil, err
	}
	// Cascade: aplica a nova rate em plan_prices pra que os cards reflitam
	// o preço imediatamente. Falha aqui é warning — a moeda já foi salva e
	// a UI cai no fallback rate-based via priceFor (front).
	if s.plans != nil {
		_ = s.plans.RecomputePricesForCurrency(ctx, updated.Code, updated.Rate, updated.Decimals)
	}
	return updated, nil
}

// Quote é a conversão de um preço base (USD cents) para a moeda de exibição
// escolhida e a moeda de liquidação correspondente.
type Quote struct {
	DisplayCurrency    string `json:"display_currency"`
	DisplaySymbol      string `json:"display_symbol"`
	DisplayAmount      string `json:"display_amount"`
	SettlementCurrency string `json:"settlement_currency"`
	SettlementSymbol   string `json:"settlement_symbol"`
	SettlementAmount   string `json:"settlement_amount"`
}

// QuoteForPlan resolve o preço de exibição e de liquidação de um plano usando
// os preços manuais por moeda (prices). Se a moeda escolhida não tiver preço
// manual, faz fallback para conversão a partir do USD (usdCents). Resolve a
// moeda de liquidação (ex.: USDT exibe, USDT cobra). Moeda inválida cai em USD.
func (s *CurrencyService) QuoteForPlan(ctx context.Context, prices map[string]string, usdCents int, displayCode string) (Quote, error) {
	if displayCode == "" {
		displayCode = "USD"
	}
	display, err := s.repo.GetByCode(ctx, displayCode)
	if err != nil || !display.DisplayEnabled {
		display, err = s.repo.GetByCode(ctx, "USD")
		if err != nil {
			return Quote{}, err
		}
	}
	settle, err := s.repo.GetByCode(ctx, display.SettlementCode)
	if err != nil {
		settle = display
	}
	return Quote{
		DisplayCurrency:    display.Code,
		DisplaySymbol:      display.Symbol,
		DisplayAmount:      amountFor(prices, usdCents, *display),
		SettlementCurrency: settle.Code,
		SettlementSymbol:   settle.Symbol,
		SettlementAmount:   amountFor(prices, usdCents, *settle),
	}, nil
}

// amountFor devolve o preço manual da moeda se existir; senão converte do USD
// usando o rate (= unidades da moeda por 1 USD).
func amountFor(prices map[string]string, usdCents int, c domain.Currency) string {
	if v, ok := prices[c.Code]; ok && v != "" {
		return v
	}
	amount := float64(usdCents) / 100.0 * c.Rate
	return strconv.FormatFloat(amount, 'f', c.Decimals, 64)
}
