package domain

import "context"

// TaxRate — alíquota fiscal por país. Foco inicial: VAT UE + GB (Fase 5.3).
// rate_pct vem do banco como NUMERIC(5,2) e fica no domínio como float64
// (sufficient pra cálculo de cents — máximo erro ~0.005¢ por linha). Para
// cálculos críticos (settlement, invoice), use round meio-cima em centavos
// inteiros em TaxService.ComputeTax — não exponha float pra fora dele.
//
// rate_type é grão de evolução: hoje só 'VAT'; amanhã 'GST' (AU/NZ), 'IVA'
// (BR/MX), 'sales_tax' (US state-level). Front trata como label opaco.
type TaxRate struct {
	CountryCode string  `json:"country_code"`
	RatePct     float64 `json:"rate_pct"`
	RateType    string  `json:"rate_type"`
}

// TaxRateRepository — porta de saída. GetByCountry é o caminho quente
// (chamado por TaxService durante checkout); List serve o endpoint público
// /v1/tax-rates pra que o front pre-compute o display do VAT antes do
// submit. Tabela pequena (<40 linhas) — sem paginação, sem filtros.
type TaxRateRepository interface {
	GetByCountry(ctx context.Context, code string) (*TaxRate, error)
	List(ctx context.Context) ([]TaxRate, error)
}
