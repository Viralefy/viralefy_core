package domain

import (
	"context"
	"time"
)

type Plan struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Category     string `json:"category"`
	Platform     string `json:"platform"`    // instagram | tiktok
	TargetType   string `json:"target_type"` // profile | publication
	FollowersQty int    `json:"followers_qty"`
	PriceCents   int    `json:"price_cents"`
	Currency     string `json:"currency"`
	Active       bool   `json:"active"`
	SortOrder    int    `json:"sort_order"`
	// Prices é o preço manual por moeda (currency_code -> valor string).
	// BRL é a base de contabilidade (espelha PriceCents).
	Prices    map[string]string `json:"prices"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	// Aggregate de reviews — populado on-demand pelo handler público
	// (ListPublicPlans) e usado pelo front pra renderizar aggregateRating
	// no JSON-LD. nil quando não há reviews visíveis ainda.
	AggregateRating *AggregateRating `json:"aggregate_rating,omitempty"`
}

type PlanRepository interface {
	ListActive(ctx context.Context) ([]Plan, error)
	ListAll(ctx context.Context) ([]Plan, error)
	GetByID(ctx context.Context, id string) (*Plan, error)
	Create(ctx context.Context, p Plan) error
	Update(ctx context.Context, p Plan) error
	Delete(ctx context.Context, id string) error
	UpsertPrices(ctx context.Context, planID string, prices map[string]string) error
	// RecomputePricesForCurrency atualiza plan_prices.amount para TODOS os
	// planos numa moeda dada, derivando de price_cents × rate. Chamado pelo
	// CurrencyService.Update — sem isso, mudar a quotação no backoffice não
	// reflete nos cards de plano (regressão 2026-06-06).
	RecomputePricesForCurrency(ctx context.Context, code string, rate float64, decimals int) error
	// RecomputePricesForPlan atualiza plan_prices.amount em TODAS as moedas
	// pra UM plano. Chamado pelo PlanService.Update/Create — sem isso, mudar
	// só price_cents (sem `in.Prices`) deixa plan_prices stale para o resto
	// das moedas (drift detectado em 2026-06-06).
	RecomputePricesForPlan(ctx context.Context, planID string) error
}
