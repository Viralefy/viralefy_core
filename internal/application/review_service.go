package application

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// ReviewService encapsula a regra de submissão de reviews: o user precisa
// ser dono do order, o order precisa estar paid, e só pode rolar 1 review
// por order (UNIQUE constraint no DB + checagem antes do INSERT pra erro
// de domínio limpo).

type ReviewService struct {
	reviews domain.ReviewRepository
	orders  domain.OrderRepository
	plans   domain.PlanRepository
}

func NewReviewService(reviews domain.ReviewRepository, orders domain.OrderRepository, plans domain.PlanRepository) *ReviewService {
	return &ReviewService{reviews: reviews, orders: orders, plans: plans}
}

type CreateReviewInput struct {
	UserID      string
	OrderID     string
	Rating      int
	Title       string
	Body        string
	CountryCode string // capturado do request (ex.: pelo X-Country header ou body)
}

// Create valida e persiste o review. Retorna ErrInvalidInput / ErrForbidden /
// ErrConflict conforme a falha — o handler HTTP mapeia em 422 / 403 / 409.
func (s *ReviewService) Create(ctx context.Context, in CreateReviewInput) (*domain.Review, error) {
	if in.UserID == "" || in.OrderID == "" {
		return nil, domain.ErrInvalidInput
	}
	if in.Rating < 1 || in.Rating > 5 {
		return nil, domain.ErrInvalidInput
	}
	title := strings.TrimSpace(in.Title)
	body := strings.TrimSpace(in.Body)
	if len(title) > 120 {
		title = title[:120]
	}
	if len(body) > 2000 {
		body = body[:2000]
	}

	order, err := s.orders.GetByID(ctx, in.OrderID)
	if err != nil {
		return nil, err
	}
	if order.UserID != in.UserID {
		return nil, domain.ErrForbidden
	}
	if order.Status != domain.OrderStatusPaid {
		// Só rola review depois que o serviço foi entregue.
		return nil, domain.ErrInvalidInput
	}

	// Idempotência: o UNIQUE no DB cobre a corrida, mas pré-checar evita
	// alocar UUID por nada.
	if existing, err := s.reviews.GetByOrderID(ctx, in.OrderID); err == nil && existing != nil {
		return nil, domain.ErrConflict
	}

	plan, err := s.plans.GetByID(ctx, order.PlanID)
	if err != nil {
		return nil, err
	}

	country := strings.ToLower(strings.TrimSpace(in.CountryCode))
	if country == "" {
		country = "us" // fallback neutro pra reviews sem origem clara
	}

	rev := domain.Review{
		ID:           uuid.NewString(),
		UserID:       in.UserID,
		OrderID:      in.OrderID,
		PlanID:       plan.ID,
		PlanCategory: plan.Category,
		CountryCode:  country,
		Rating:       in.Rating,
		Title:        title,
		Body:         body,
		Visible:      true,
	}
	if err := s.reviews.Create(ctx, rev); err != nil {
		return nil, err
	}
	return &rev, nil
}

func (s *ReviewService) GetByOrder(ctx context.Context, orderID string) (*domain.Review, error) {
	return s.reviews.GetByOrderID(ctx, orderID)
}

func (s *ReviewService) ListByPlan(ctx context.Context, planID string, limit int) ([]domain.PublicReview, error) {
	return s.reviews.ListPublicByPlan(ctx, planID, limit)
}

func (s *ReviewService) AggregateByPlan(ctx context.Context, planID string) (*domain.AggregateRating, error) {
	return s.reviews.AggregateByPlan(ctx, planID)
}

func (s *ReviewService) AggregateByCategory(ctx context.Context, category string) (*domain.AggregateRating, error) {
	return s.reviews.AggregateByCategory(ctx, category)
}

// SetVisibility é a moderação (admin only, gated por permission no handler).
func (s *ReviewService) SetVisibility(ctx context.Context, id string, visible bool) error {
	return s.reviews.SetVisibility(ctx, id, visible)
}

// AdminList — moderação. Filtros opcionais (apenas escondidos, por plano,
// por categoria). Limita a 200 por chamada (handler aceita query param).
func (s *ReviewService) AdminList(ctx context.Context, filter domain.AdminReviewFilter, limit int) ([]domain.AdminReview, error) {
	return s.reviews.ListAdmin(ctx, filter, limit)
}

func (s *ReviewService) AdminGet(ctx context.Context, id string) (*domain.Review, error) {
	return s.reviews.GetByID(ctx, id)
}
