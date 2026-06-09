package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type CouponRepo struct {
	db *DB
}

func NewCouponRepo(db *DB) *CouponRepo {
	return &CouponRepo{db: db}
}

const couponCols = `code, description, discount_type, discount_value, max_uses, used_count,
                    valid_from, valid_until, min_order_usd_cents, only_categories,
                    only_first_order, active, created_at, updated_at`

func scanCoupon(row pgx.Row) (*domain.Coupon, error) {
	var c domain.Coupon
	err := row.Scan(
		&c.Code, &c.Description, &c.DiscountType, &c.DiscountValue,
		&c.MaxUses, &c.UsedCount,
		&c.ValidFrom, &c.ValidUntil,
		&c.MinOrderUSDCents, &c.OnlyCategories,
		&c.OnlyFirstOrder, &c.Active,
		&c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *CouponRepo) GetByCode(ctx context.Context, code string) (*domain.Coupon, error) {
	row := r.db.pool.QueryRow(ctx,
		`SELECT `+couponCols+` FROM coupons WHERE code = $1`,
		domain.NormalizeCode(code),
	)
	c, err := scanCoupon(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrCouponNotFound
	}
	return c, err
}

func (r *CouponRepo) Create(ctx context.Context, c domain.Coupon) error {
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO coupons (code, description, discount_type, discount_value,
		                     max_uses, valid_from, valid_until,
		                     min_order_usd_cents, only_categories,
		                     only_first_order, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		domain.NormalizeCode(c.Code), c.Description, string(c.DiscountType), c.DiscountValue,
		c.MaxUses, c.ValidFrom, c.ValidUntil,
		c.MinOrderUSDCents, c.OnlyCategories,
		c.OnlyFirstOrder, c.Active,
	)
	return err
}

func (r *CouponRepo) Update(ctx context.Context, c domain.Coupon) error {
	_, err := r.db.pool.Exec(ctx, `
		UPDATE coupons SET
			description = $2, discount_type = $3, discount_value = $4,
			max_uses = $5, valid_from = $6, valid_until = $7,
			min_order_usd_cents = $8, only_categories = $9,
			only_first_order = $10, active = $11,
			updated_at = NOW()
		WHERE code = $1`,
		domain.NormalizeCode(c.Code), c.Description, string(c.DiscountType), c.DiscountValue,
		c.MaxUses, c.ValidFrom, c.ValidUntil,
		c.MinOrderUSDCents, c.OnlyCategories,
		c.OnlyFirstOrder, c.Active,
	)
	return err
}

func (r *CouponRepo) List(ctx context.Context) ([]domain.Coupon, error) {
	rows, err := r.db.pool.Query(ctx,
		`SELECT `+couponCols+` FROM coupons ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Coupon{}
	for rows.Next() {
		c, err := scanCoupon(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// IncrementUsedCount tenta atomicamente bumpar used_count respeitando max_uses.
// Sucesso → 1 row updated. Falha (teto) → 0 rows → ErrCouponMaxUsesReached.
// Cobre race entre 2 redeem simultâneos no último slot.
func (r *CouponRepo) IncrementUsedCount(ctx context.Context, code string) error {
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE coupons
		   SET used_count = used_count + 1, updated_at = NOW()
		 WHERE code = $1
		   AND active = TRUE
		   AND (max_uses IS NULL OR used_count < max_uses)`,
		domain.NormalizeCode(code),
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Pode ser inactive, deletado, ou max atingido. Cliente leu o coupon
		// antes — se chegou aqui sem encontrar, é max atingido.
		return domain.ErrCouponMaxUsesReached
	}
	return nil
}

func (r *CouponRepo) CreateRedemption(ctx context.Context, rd domain.CouponRedemption) error {
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO coupon_redemptions (id, coupon_code, order_id, user_email, discount_usd_cents)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (order_id) DO NOTHING`,
		rd.ID, domain.NormalizeCode(rd.CouponCode), rd.OrderID, rd.UserEmail, rd.DiscountUSDCents,
	)
	return err
}

func (r *CouponRepo) HasUserPaidOrder(ctx context.Context, email string) (bool, error) {
	var exists bool
	err := r.db.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM orders o
			JOIN users u ON u.id = o.user_id
			WHERE u.email = $1 AND o.status = 'paid'
		)`, email,
	).Scan(&exists)
	return exists, err
}
