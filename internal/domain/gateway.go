package domain

import (
	"context"
	"time"
)

type PaymentGateway struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Provider  string            `json:"provider"`
	Active    bool              `json:"active"`
	Config    map[string]string `json:"config"`
	// AcceptedCurrencies lista os códigos ISO que esse gateway pode liquidar.
	// Ex.: Woovi=["BRL"], Heleket=["USDT","BTC","USD"], ManualPIX=["BRL"].
	// O checkout filtra gateways por essa lista antes de cobrar.
	AcceptedCurrencies []string  `json:"accepted_currencies"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type GatewayRepository interface {
	ListAll(ctx context.Context) ([]PaymentGateway, error)
	GetByID(ctx context.Context, id string) (*PaymentGateway, error)
	Create(ctx context.Context, g PaymentGateway) error
	Update(ctx context.Context, g PaymentGateway) error
	Delete(ctx context.Context, id string) error
	GetDefaultActive(ctx context.Context) (*PaymentGateway, error)
	GetActiveByProvider(ctx context.Context, provider string) (*PaymentGateway, error)
}
