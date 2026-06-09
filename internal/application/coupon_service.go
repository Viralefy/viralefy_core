package application

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// CouponService valida e aplica cupons no checkout + CRUD admin.
type CouponService struct {
	repo  domain.CouponRepository
	now   func() time.Time
}

func NewCouponService(repo domain.CouponRepository) *CouponService {
	return &CouponService{repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

// PreviewInput é o que o front passa em /v1/coupons/validate antes de submeter o checkout.
type PreviewInput struct {
	Code         string
	AmountUSDCents int
	PlanCategory string
	UserEmail    string // opcional — sem ele, only_first_order é tratado como "skip"
}

// PreviewResult é o que devolvemos pro front mostrar "discount: $X off".
type PreviewResult struct {
	Code              string `json:"code"`
	DiscountUSDCents  int    `json:"discount_usd_cents"`
	FinalUSDCents     int    `json:"final_usd_cents"`
	Description       string `json:"description"`
}

// Preview NÃO redeem — só calcula. Idempotente, seguro para repetir.
func (s *CouponService) Preview(ctx context.Context, in PreviewInput) (*PreviewResult, error) {
	c, err := s.repo.GetByCode(ctx, in.Code)
	if err != nil {
		return nil, err
	}
	if err := c.Validate(s.now(), in.AmountUSDCents, in.PlanCategory); err != nil {
		return nil, err
	}
	if c.MaxUses != nil && c.UsedCount >= *c.MaxUses {
		return nil, domain.ErrCouponMaxUsesReached
	}
	if c.OnlyFirstOrder && in.UserEmail != "" {
		had, err := s.repo.HasUserPaidOrder(ctx, strings.ToLower(in.UserEmail))
		if err != nil {
			return nil, err
		}
		if had {
			return nil, domain.ErrCouponFirstOrderOnly
		}
	}
	discount := c.Apply(in.AmountUSDCents)
	if discount <= 0 {
		// Defensive: não devolve cupom que não desconta nada.
		return nil, domain.ErrCouponInactive
	}
	return &PreviewResult{
		Code:             c.Code,
		DiscountUSDCents: discount,
		FinalUSDCents:    in.AmountUSDCents - discount,
		Description:      c.Description,
	}, nil
}

// Redeem aplica o cupom no order. Chamado pelo CheckoutService DENTRO da
// transação do checkout (ou logo após criar o order). Garante atomicidade:
//   1. IncrementUsedCount (cobertura de race no último slot)
//   2. CreateRedemption (UNIQUE em order_id — 1 cupom por order)
// Se algum dos dois falhar, propaga erro. CheckoutService trata.
type RedeemInput struct {
	Code             string
	OrderID          string
	UserEmail        string
	DiscountUSDCents int
}

func (s *CouponService) Redeem(ctx context.Context, in RedeemInput) error {
	if err := s.repo.IncrementUsedCount(ctx, in.Code); err != nil {
		return err
	}
	return s.repo.CreateRedemption(ctx, domain.CouponRedemption{
		ID:               uuid.New().String(),
		CouponCode:       domain.NormalizeCode(in.Code),
		OrderID:          in.OrderID,
		UserEmail:        strings.ToLower(in.UserEmail),
		DiscountUSDCents: in.DiscountUSDCents,
	})
}

// Admin ops ----

func (s *CouponService) Create(ctx context.Context, c domain.Coupon) (*domain.Coupon, error) {
	if c.Code == "" || c.DiscountValue == 0 {
		return nil, domain.ErrInvalidInput
	}
	if c.ValidFrom.IsZero() {
		c.ValidFrom = s.now()
	}
	c.Active = true
	if err := s.repo.Create(ctx, c); err != nil {
		return nil, err
	}
	return s.repo.GetByCode(ctx, c.Code)
}

func (s *CouponService) Update(ctx context.Context, c domain.Coupon) (*domain.Coupon, error) {
	if err := s.repo.Update(ctx, c); err != nil {
		return nil, err
	}
	return s.repo.GetByCode(ctx, c.Code)
}

func (s *CouponService) List(ctx context.Context) ([]domain.Coupon, error) {
	return s.repo.List(ctx)
}

func (s *CouponService) Get(ctx context.Context, code string) (*domain.Coupon, error) {
	return s.repo.GetByCode(ctx, code)
}
