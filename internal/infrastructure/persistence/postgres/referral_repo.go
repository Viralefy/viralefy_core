package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type ReferralRepo struct{ db *DB }

func NewReferralRepo(db *DB) *ReferralRepo { return &ReferralRepo{db: db} }

// codeAlphabet — base32 sem chars ambíguos (sem 0/O/1/I/L). Espaço pra
// "VIRA"+6 chars ≈ 28^6 ≈ 481M variações: colisão prática ≈ 0.
const codeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

func randCode(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = codeAlphabet[int(b)%len(codeAlphabet)]
	}
	return string(out), nil
}

// EnsureCode garante código pro user. Idempotente: se já existir, devolve.
// Em conflito (colisão de código), tenta até 3 vezes.
func (r *ReferralRepo) EnsureCode(ctx context.Context, userID string) (string, error) {
	// Caminho rápido: já tem código?
	var existing *string
	if err := r.db.pool.QueryRow(ctx,
		`SELECT referral_code FROM users WHERE id = $1`, userID,
	).Scan(&existing); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrNotFound
		}
		return "", err
	}
	if existing != nil && *existing != "" {
		return *existing, nil
	}

	// Gera + tenta inserir. Em conflito (race ou colisão), reentra.
	for attempt := 0; attempt < 3; attempt++ {
		suffix, err := randCode(6)
		if err != nil {
			return "", err
		}
		code := "VIRA" + suffix
		tag, err := r.db.pool.Exec(ctx,
			`UPDATE users SET referral_code = $2
			   WHERE id = $1 AND referral_code IS NULL`,
			userID, code,
		)
		if err != nil {
			// 23505 unique_violation — outro user pegou o código primeiro.
			if strings.Contains(err.Error(), "referral_code") &&
				strings.Contains(err.Error(), "duplicate") {
				continue
			}
			return "", err
		}
		if tag.RowsAffected() == 0 {
			// Concorrente populou referral_code antes — re-lê.
			var cur *string
			if err := r.db.pool.QueryRow(ctx,
				`SELECT referral_code FROM users WHERE id = $1`, userID,
			).Scan(&cur); err == nil && cur != nil {
				return *cur, nil
			}
			continue
		}
		return code, nil
	}
	return "", errors.New("referral: failed to allocate code after retries")
}

func (r *ReferralRepo) GetByUserCode(ctx context.Context, code string) (*domain.User, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return nil, domain.ErrNotFound
	}
	row := r.db.pool.QueryRow(ctx, `
		SELECT id, email, name, instagram, password_hash, created_at, tracking_data
		  FROM users WHERE referral_code = $1`, code)
	return scanUser(row)
}

func (r *ReferralRepo) SetReferredBy(ctx context.Context, userID, referrerID string) error {
	if userID == "" || referrerID == "" || userID == referrerID {
		return domain.ErrInvalidInput
	}
	// First-touch wins: só seta quando ainda é NULL.
	_, err := r.db.pool.Exec(ctx, `
		UPDATE users SET referred_by_user_id = $2
		  WHERE id = $1 AND referred_by_user_id IS NULL`,
		userID, referrerID,
	)
	return err
}

func (r *ReferralRepo) GrantReward(ctx context.Context, in domain.GrantRewardInput) error {
	if in.RewardUSDCents <= 0 {
		return domain.ErrInvalidInput
	}
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO referral_rewards (id, referrer_user_id, referred_user_id, order_id, reward_usd_cents)
		VALUES ($1, $2, $3, $4, $5)`,
		// id passa do service (uuid).
		newID(in), in.ReferrerUserID, in.ReferredUserID, in.OrderID, in.RewardUSDCents,
	)
	if err != nil {
		// UNIQUE(order_id) → conflict significa que já recompensamos.
		if strings.Contains(err.Error(), "referral_rewards") &&
			strings.Contains(err.Error(), "duplicate") {
			return domain.ErrConflict
		}
		return err
	}
	return nil
}

// newID gera ID determinístico-ish a partir do input — na prática service
// passa um uuid próprio via campo extra. Aqui reusa OrderID como prefixo
// pra facilitar troubleshoot via grep.
func newID(in domain.GrantRewardInput) string {
	return "rr_" + in.OrderID
}

// FetchReferrerID retorna users.referred_by_user_id ("" se NULL).
// Métido auxiliar pra application.ReferralService — fora da interface
// principal porque é detalhe interno do hook GrantOnFirstPaidOrder.
func (r *ReferralRepo) FetchReferrerID(ctx context.Context, userID string) (string, error) {
	var ref *string
	if err := r.db.pool.QueryRow(ctx,
		`SELECT referred_by_user_id FROM users WHERE id = $1`, userID,
	).Scan(&ref); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	if ref == nil {
		return "", nil
	}
	return *ref, nil
}

// CountPriorPaidOrders conta orders paid de user, excluindo a order
// passada — usado pra detectar "esse é o primeiro paid?".
func (r *ReferralRepo) CountPriorPaidOrders(ctx context.Context, userID, excludeOrderID string) (int, error) {
	var n int
	err := r.db.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM orders
		 WHERE user_id = $1 AND status = 'paid' AND id <> $2`,
		userID, excludeOrderID,
	).Scan(&n)
	return n, err
}

func (r *ReferralRepo) Stats(ctx context.Context, userID string) (int, int64, error) {
	var totalReferred int
	if err := r.db.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM users WHERE referred_by_user_id = $1`, userID,
	).Scan(&totalReferred); err != nil {
		return 0, 0, err
	}
	var totalEarned int64
	if err := r.db.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(reward_usd_cents), 0)
		   FROM referral_rewards WHERE referrer_user_id = $1`, userID,
	).Scan(&totalEarned); err != nil {
		return 0, 0, err
	}
	return totalReferred, totalEarned, nil
}
