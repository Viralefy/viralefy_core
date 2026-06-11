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
	password_hash, created_at, tracking_data,
	deleted_at, deleted_by_admin_id, delete_reason`

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
		&u.DeletedAt, &u.DeletedByAdminID, &u.DeleteReason,
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
		       u.created_at, COALESCE(c.balance_cents, 0),
		       u.deleted_at, u.deleted_by_admin_id, u.delete_reason
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
		if err := rows.Scan(&v.ID, &v.Email, &v.Name, &v.Instagram, &v.Phone, &v.Telegram, &v.CreatedAt, &v.BalanceCents,
			&v.DeletedAt, &v.DeletedByAdminID, &v.DeleteReason); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// SoftDeleteUser marca usuário como apagado. Vide order_repo.go pra contrato.
// Idempotente — não sobrescreve trilha original.
//
// NOTA: depois do soft-delete, queries de login (viralefy_auth) já filtram
// DeletedAt != NULL e bloqueiam sessão. /v1/me/* responde 401 igualmente
// porque o token original ainda é válido até expirar (TTL 15min) — pra
// invalidar imediatamente, o admin deve usar a tela de admin sessions
// (PHASE-9 hot-set, fora do escopo deste PR).
func (r *UserRepo) SoftDeleteUser(ctx context.Context, id, adminID, reason string) error {
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE users
		   SET deleted_at = COALESCE(deleted_at, NOW()),
		       deleted_by_admin_id = COALESCE(deleted_by_admin_id, $2),
		       delete_reason = COALESCE(delete_reason, NULLIF($3, ''))
		 WHERE id = $1`, id, adminID, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// HardDeleteUser remove a row do DB. Só superadmin. O ON DELETE CASCADE
// dos FKs encadeia em orders, invoices, profiles, reviews, tickets — em
// outras palavras, EXPURGO TOTAL. UI deve sempre confirmar antes de chamar.
func (r *UserRepo) HardDeleteUser(ctx context.Context, id string) error {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// RestoreUser tira o soft-delete (deleted_at = NULL). Idempotente.
func (r *UserRepo) RestoreUser(ctx context.Context, id string) error {
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE users SET deleted_at=NULL, deleted_by_admin_id=NULL, delete_reason=NULL
		WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
