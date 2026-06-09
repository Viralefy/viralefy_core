package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type UserRepo struct{ db *DB }

func NewUserRepo(db *DB) *UserRepo { return &UserRepo{db: db} }

const userCols = `id, email, name, instagram,
	COALESCE(phone, ''), COALESCE(telegram, ''),
	password_hash, created_at, tracking_data`

func (r *UserRepo) Create(ctx context.Context, u domain.User) error {
	tracking, _ := json.Marshal(u.TrackingData)
	if len(tracking) == 0 {
		tracking = []byte("{}")
	}
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO users (id, email, name, instagram, phone, telegram, password_hash, tracking_data)
		VALUES ($1,$2,$3,$4, NULLIF($5,''), NULLIF($6,''), $7, $8)`,
		u.ID, u.Email, u.Name, u.Instagram, u.Phone, u.Telegram, u.PasswordHash, tracking)
	return err
}

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	row := r.db.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email=$1`, email)
	return scanUser(row)
}

func (r *UserRepo) GetByID(ctx context.Context, id string) (*domain.User, error) {
	row := r.db.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id)
	return scanUser(row)
}

func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	var tracking []byte
	err := row.Scan(
		&u.ID, &u.Email, &u.Name, &u.Instagram,
		&u.Phone, &u.Telegram,
		&u.PasswordHash, &u.CreatedAt, &tracking,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err == nil && len(tracking) > 0 {
		u.TrackingData = map[string]any{}
		_ = json.Unmarshal(tracking, &u.TrackingData)
	}
	return &u, err
}

// ListWithCreditBalance — usado pelo backoffice. LEFT JOIN no credit_accounts
// (saldo 0 quando o usuário ainda não fez recarga).
func (r *UserRepo) ListWithCreditBalance(ctx context.Context, limit int) ([]domain.UserView, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := r.db.pool.Query(ctx, `
		SELECT u.id, u.email, u.name, u.instagram,
		       COALESCE(u.phone, ''), COALESCE(u.telegram, ''),
		       u.created_at, COALESCE(c.balance_cents, 0)
		FROM users u
		LEFT JOIN credit_accounts c ON c.user_id = u.id
		ORDER BY u.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []domain.UserView{}
	for rows.Next() {
		var v domain.UserView
		if err := rows.Scan(&v.ID, &v.Email, &v.Name, &v.Instagram, &v.Phone, &v.Telegram, &v.CreatedAt, &v.BalanceCents); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}
