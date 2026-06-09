package application

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// TaxService — autoridade de cálculo de impostos no checkout (Fase 5.3).
//
// Política:
//   - País fora do catálogo (não EU/GB) → tax = 0, rate = 0, sem erro.
//   - País no catálogo → tax_usd_cents = round(amount * rate_pct / 100).
//   - Arredondamento: meio-cima em centavos inteiros (math.Round). Erro
//     fiscal máximo por linha = 0.5¢, dentro da tolerância UE de quebra
//     de centavo. Settlement em USDT canônico inclui o tax integralmente.
//
// O service não muta orders nem grava em banco — responsabilidade do
// CheckoutService (que é arquivo proibido neste agente; integração via
// main loop / Wave3.5). Esse contrato é puro: input cents, output cents.
type TaxService struct {
	repo domain.TaxRateRepository
}

func NewTaxService(repo domain.TaxRateRepository) *TaxService {
	return &TaxService{repo: repo}
}

// ComputeTax calcula o VAT a aplicar sobre amountUSDCents para um país.
// Retorna (taxUSDCents=0, ratePct=0, nil) quando o país não está no
// catálogo — é o caso comum (US, BR, IN, etc.) e NÃO é erro.
//
// amountUSDCents é o net antes do imposto. Para checkout, o caller deve
// computar amount_charged = amountUSDCents + taxUSDCents. orders.tax_*
// guardam country_code, rate_pct e tax_usd_cents pra auditoria/invoice.
func (s *TaxService) ComputeTax(ctx context.Context, countryCode string, amountUSDCents int) (taxUSDCents int, ratePct float64, err error) {
	if s == nil || s.repo == nil {
		return 0, 0, nil
	}
	cc := strings.ToLower(strings.TrimSpace(countryCode))
	if cc == "" || amountUSDCents <= 0 {
		return 0, 0, nil
	}
	rate, err := s.repo.GetByCountry(ctx, cc)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	if rate.RatePct <= 0 {
		return 0, rate.RatePct, nil
	}
	// math.Round arredonda meio-cima (banker's rounding NÃO — preferimos
	// determinismo legal: 0.5¢ sempre sobe). Cast pra int trunca depois do
	// round, então o resultado é sempre o inteiro mais próximo.
	tax := math.Round(float64(amountUSDCents) * rate.RatePct / 100.0)
	if tax < 0 {
		tax = 0
	}
	return int(tax), rate.RatePct, nil
}
