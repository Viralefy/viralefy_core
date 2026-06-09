package application

import (
	"context"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type PlanService struct {
	repo domain.PlanRepository
}

func NewPlanService(repo domain.PlanRepository) *PlanService {
	return &PlanService{repo: repo}
}

// GetByID expõe o repo pra handlers que precisam de snapshot pre/post-mutation
// (audit log). Idêntico ao repo, só pra não vazar repo nos handlers.
func (s *PlanService) GetByID(ctx context.Context, id string) (*domain.Plan, error) {
	return s.repo.GetByID(ctx, id)
}

// ListByCategory devolve os planos ativos de uma categoria. Usado pelo
// handler de Account Recovery pra encontrar o plano-âncora da categoria.
func (s *PlanService) ListByCategory(ctx context.Context, category string) ([]domain.Plan, error) {
	all, err := s.repo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Plan, 0, 4)
	for _, p := range all {
		if p.Category == category {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *PlanService) ListPublic(ctx context.Context) ([]domain.Plan, error) {
	return s.repo.ListActive(ctx)
}

func (s *PlanService) ListAdmin(ctx context.Context) ([]domain.Plan, error) {
	return s.repo.ListAll(ctx)
}

type CreatePlanInput struct {
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Category     string            `json:"category"`
	FollowersQty int               `json:"followers_qty"`
	PriceCents   int               `json:"price_cents"`
	Currency     string            `json:"currency"`
	Active       bool              `json:"active"`
	SortOrder    int               `json:"sort_order"`
	Prices       map[string]string `json:"prices"` // preço manual por moeda
}

func (s *PlanService) Create(ctx context.Context, in CreatePlanInput) (*domain.Plan, error) {
	// Preço base USD pode vir em PriceCents ou em Prices["USD"]. USD é a
	// moeda canônica do sistema; BRL/EUR/USDT são derivadas via plan_prices.
	if cents, ok := usdCents(in.Prices); ok {
		in.PriceCents = cents
	}
	if in.Name == "" || in.FollowersQty <= 0 || in.PriceCents <= 0 {
		return nil, domain.ErrInvalidInput
	}
	currency := in.Currency
	if currency == "" {
		currency = "USD"
	}
	category := in.Category
	if category == "" {
		category = "seguidores_instagram"
	}
	p := domain.Plan{
		ID:           uuid.New().String(),
		Name:         in.Name,
		Description:  in.Description,
		Category:     category,
		FollowersQty: in.FollowersQty,
		PriceCents:   in.PriceCents,
		Currency:     currency,
		Active:       in.Active,
		SortOrder:    in.SortOrder,
	}
	if err := s.repo.Create(ctx, p); err != nil {
		return nil, err
	}
	// Ordem importa: 1º baseline auto-fill (USD/100 * rate em TODAS as moedas),
	// 2º overrides manuais do admin (UpsertPrices). Se invertesse, o baseline
	// sobrescrevia o override.
	_ = s.repo.RecomputePricesForPlan(ctx, p.ID)
	prices := withUSD(in.Prices, in.PriceCents)
	if err := s.repo.UpsertPrices(ctx, p.ID, prices); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, p.ID)
}

type UpdatePlanInput struct {
	ID           string            `json:"-"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Category     string            `json:"category"`
	FollowersQty int               `json:"followers_qty"`
	PriceCents   int               `json:"price_cents"`
	Currency     string            `json:"currency"`
	Active       bool              `json:"active"`
	SortOrder    int               `json:"sort_order"`
	Prices       map[string]string `json:"prices"`
}

func (s *PlanService) Update(ctx context.Context, in UpdatePlanInput) (*domain.Plan, error) {
	existing, err := s.repo.GetByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if cents, ok := usdCents(in.Prices); ok {
		in.PriceCents = cents
	}
	if in.Name != "" {
		existing.Name = in.Name
	}
	if in.Description != "" {
		existing.Description = in.Description
	}
	if in.Category != "" {
		existing.Category = in.Category
	}
	if in.FollowersQty > 0 {
		existing.FollowersQty = in.FollowersQty
	}
	if in.PriceCents > 0 {
		existing.PriceCents = in.PriceCents
	}
	if in.Currency != "" {
		existing.Currency = in.Currency
	}
	existing.Active = in.Active
	existing.SortOrder = in.SortOrder
	if err := s.repo.Update(ctx, *existing); err != nil {
		return nil, err
	}
	// SEMPRE re-baselineia: price_cents pode ter mudado, e plan_prices fica
	// stale se in.Prices não veio populado (drift detectado em 2026-06-06,
	// onde editar só o price_cents pela UI deixava as moedas defasadas).
	_ = s.repo.RecomputePricesForPlan(ctx, existing.ID)
	if len(in.Prices) > 0 {
		if err := s.repo.UpsertPrices(ctx, existing.ID, withUSD(in.Prices, existing.PriceCents)); err != nil {
			return nil, err
		}
	}
	return s.repo.GetByID(ctx, existing.ID)
}

func (s *PlanService) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// usdCents extrai o preço USD do mapa de preços manuais (ex.: "2.50" -> 250).
// USD é a moeda base canônica do sistema.
func usdCents(prices map[string]string) (int, bool) {
	v, ok := prices["USD"]
	if !ok || v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(v, ",", "."), 64)
	if err != nil {
		return 0, false
	}
	return int(math.Round(f * 100)), true
}

// withUSD garante que USD esteja presente no mapa, derivando de price_cents.
func withUSD(prices map[string]string, cents int) map[string]string {
	out := map[string]string{}
	for k, v := range prices {
		out[k] = v
	}
	if _, ok := out["USD"]; !ok {
		out["USD"] = strconv.FormatFloat(float64(cents)/100.0, 'f', 2, 64)
	}
	return out
}
