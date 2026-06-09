package domain

import (
	"context"
	"errors"
	"strings"
	"time"
)

// DiscountType discriminator pra Coupon.DiscountValue.
type DiscountType string

const (
	DiscountTypePercent       DiscountType = "percent"
	DiscountTypeFixedUSDCents DiscountType = "fixed_usd_cents"
)

// Coupon — promoção aplicada no checkout.
type Coupon struct {
	Code              string       `json:"code"`
	Description       string       `json:"description"`
	DiscountType      DiscountType `json:"discount_type"`
	DiscountValue     int          `json:"discount_value"`
	MaxUses           *int         `json:"max_uses,omitempty"`
	UsedCount         int          `json:"used_count"`
	ValidFrom         time.Time    `json:"valid_from"`
	ValidUntil        *time.Time   `json:"valid_until,omitempty"`
	MinOrderUSDCents  *int         `json:"min_order_usd_cents,omitempty"`
	OnlyCategories    []string     `json:"only_categories,omitempty"`
	OnlyFirstOrder    bool         `json:"only_first_order"`
	Active            bool         `json:"active"`
	CreatedAt         time.Time    `json:"created_at"`
	UpdatedAt         time.Time    `json:"updated_at"`
}

// NormalizeCode canonicaliza o código pra consultas/comparações.
// Política: uppercase + trim. Códigos "black10", "BLACK10 ", "Black10" mapeiam
// para "BLACK10".
func NormalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// Apply devolve o valor descontado (em USD cents) a partir do ticket.
// Nunca negativo: floor em 0.
func (c Coupon) Apply(amountUSDCents int) int {
	if amountUSDCents <= 0 {
		return 0
	}
	switch c.DiscountType {
	case DiscountTypePercent:
		return (amountUSDCents * c.DiscountValue) / 100
	case DiscountTypeFixedUSDCents:
		if c.DiscountValue > amountUSDCents {
			return amountUSDCents
		}
		return c.DiscountValue
	}
	return 0
}

// Validate roda as regras estáticas (estado próprio do Coupon + ticket).
// Não checa max_uses (race condition) nem first_order (depende do user).
// Quem chama (CouponService) faz essas validações com DB.
func (c Coupon) Validate(now time.Time, amountUSDCents int, planCategory string) error {
	if !c.Active {
		return ErrCouponInactive
	}
	if now.Before(c.ValidFrom) {
		return ErrCouponNotYetValid
	}
	if c.ValidUntil != nil && now.After(*c.ValidUntil) {
		return ErrCouponExpired
	}
	if c.MinOrderUSDCents != nil && amountUSDCents < *c.MinOrderUSDCents {
		return ErrCouponMinOrderNotMet
	}
	if len(c.OnlyCategories) > 0 && !contains(c.OnlyCategories, planCategory) {
		return ErrCouponCategoryNotAllowed
	}
	return nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// Errors específicos pra UX no checkout.
var (
	ErrCouponNotFound           = errors.New("coupon not found")
	ErrCouponInactive           = errors.New("coupon inactive")
	ErrCouponNotYetValid        = errors.New("coupon not yet valid")
	ErrCouponExpired            = errors.New("coupon expired")
	ErrCouponMinOrderNotMet     = errors.New("coupon requires higher order amount")
	ErrCouponCategoryNotAllowed = errors.New("coupon not applicable to this category")
	ErrCouponMaxUsesReached     = errors.New("coupon usage limit reached")
	ErrCouponFirstOrderOnly     = errors.New("coupon valid only for first order")
)

// CouponRedemption é o registro de aplicação do cupom em um order específico.
type CouponRedemption struct {
	ID               string    `json:"id"`
	CouponCode       string    `json:"coupon_code"`
	OrderID          string    `json:"order_id"`
	UserEmail        string    `json:"user_email"`
	DiscountUSDCents int       `json:"discount_usd_cents"`
	RedeemedAt       time.Time `json:"redeemed_at"`
}

// CouponRepository porta de saída para persistência.
type CouponRepository interface {
	GetByCode(ctx context.Context, code string) (*Coupon, error)
	Create(ctx context.Context, c Coupon) error
	Update(ctx context.Context, c Coupon) error
	List(ctx context.Context) ([]Coupon, error)

	// IncrementUsedCount é atomico — única forma de evitar race entre 2
	// checkouts simultâneos no último slot do cupom. Retorna ErrCouponMaxUsesReached
	// se o increment violaria o teto.
	IncrementUsedCount(ctx context.Context, code string) error

	// CreateRedemption insere o audit row. ON CONFLICT (order_id) garante
	// 1 cupom por order.
	CreateRedemption(ctx context.Context, r CouponRedemption) error

	// HasUserPaidOrder retorna true se o email tem >=1 order com status 'paid'.
	// Usado pra cupons only_first_order.
	HasUserPaidOrder(ctx context.Context, email string) (bool, error)
}
