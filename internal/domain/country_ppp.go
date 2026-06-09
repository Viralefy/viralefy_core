package domain

import "context"

// CountryPPP — multiplier de Purchasing Power Parity por país. NÃO afeta
// settlement nem o canônico USD: front aplica antes do formatBalance pra
// gerar display_amount adaptado ao poder de compra local. Multiplier 1.00
// = preço cheio (mercados T1); 0.30 = piso (mercados emergentes T3).
type CountryPPP struct {
	Code       string  `json:"country_code"`
	Multiplier float64 `json:"multiplier"`
}

// CountryPPPRepository — porta de saída. List é o método quente (servido
// pela rota pública /v1/country-ppp e cacheado no client). GetByCode fica
// reservado pra usos pontuais (ex.: enriquecimento server-side).
type CountryPPPRepository interface {
	GetByCode(ctx context.Context, code string) (*CountryPPP, error)
	List(ctx context.Context) ([]CountryPPP, error)
}
