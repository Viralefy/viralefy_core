package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type SubscriptionRepo struct{ db *DB }

func NewSubscriptionRepo(db *DB) *SubscriptionRepo { return &SubscriptionRepo{db: db} }

const subscriptionCols = `id, user_id, plan_id, status, interval,
	next_billing_at, cancelled_at, failed_payments, created_at, updated_at`

func scanSubscription(row pgx.Row) (*domain.Subscription, error) {
	var s domain.Subscription
	var status, interval string
	var cancelledAt *time.Time
	err := row.Scan(
		&s.ID, &s.UserID, &s.PlanID, &status, &interval,
		&s.NextBillingAt, &cancelledAt, &s.FailedPayments,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	s.Status = domain.SubscriptionStatus(status)
	s.Interval = interval
	s.CancelledAt = cancelledAt
	return &s, nil
}

// Create insere a sub. Upsert NÃO é feito aqui porque a regra de
// idempotência (active único por user+plan) vive no service — repo só
// reflete a tabela. Se houver conflict UNIQUE, deixa subir pra service
// decidir.
func (r *SubscriptionRepo) Create(ctx context.Context, s domain.Subscription) error {
	if s.Status == "" {
		s.Status = domain.SubscriptionStatusActive
	}
	if s.Interval == "" {
		s.Interval = "month"
	}
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO subscriptions
		    (id, user_id, plan_id, status, interval, next_billing_at, failed_payments)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		s.ID, s.UserID, s.PlanID, string(s.Status), s.Interval, s.NextBillingAt, s.FailedPayments,
	)
	return err
}

func (r *SubscriptionRepo) GetByID(ctx context.Context, id string) (*domain.Subscription, error) {
	row := r.db.pool.QueryRow(ctx,
		`SELECT `+subscriptionCols+` FROM subscriptions WHERE id = $1`, id,
	)
	s, err := scanSubscription(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return s, err
}

func (r *SubscriptionRepo) ListByUser(ctx context.Context, userID string) ([]domain.Subscription, error) {
	rows, err := r.db.pool.Query(ctx,
		`SELECT `+subscriptionCols+` FROM subscriptions
		  WHERE user_id = $1
		  ORDER BY created_at DESC`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Subscription{}
	for rows.Next() {
		s, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ListDue limita em 100 por chamada — cron tickando 1h dá vazão de 2400/dia
// por tick, o que é >>> que o esperado pro MVP. Se algum dia precisarmos
// de mais, troca por cursor paginado.
func (r *SubscriptionRepo) ListDue(ctx context.Context, before time.Time) ([]domain.Subscription, error) {
	rows, err := r.db.pool.Query(ctx, `
		SELECT `+subscriptionCols+` FROM subscriptions
		 WHERE status = 'active' AND next_billing_at <= $1
		 ORDER BY next_billing_at ASC
		 LIMIT 100`, before,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Subscription{}
	for rows.Next() {
		s, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *SubscriptionRepo) Update(ctx context.Context, s domain.Subscription) error {
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE subscriptions SET
		    status            = $2,
		    interval          = $3,
		    next_billing_at   = $4,
		    cancelled_at      = $5,
		    failed_payments   = $6,
		    updated_at        = NOW()
		 WHERE id = $1`,
		s.ID, string(s.Status), s.Interval, s.NextBillingAt, s.CancelledAt, s.FailedPayments,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *SubscriptionRepo) Cancel(ctx context.Context, id string) error {
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE subscriptions
		   SET status = 'cancelled',
		       cancelled_at = COALESCE(cancelled_at, NOW()),
		       updated_at = NOW()
		 WHERE id = $1`, id,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
