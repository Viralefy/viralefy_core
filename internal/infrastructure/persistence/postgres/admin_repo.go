package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type AdminRepo struct{ db *DB }

func NewAdminRepo(db *DB) *AdminRepo { return &AdminRepo{db: db} }

const adminCols = `id, email, password_hash, name, role,
	COALESCE(requires_2fa, TRUE) AS requires_2fa,
	created_at`

func (r *AdminRepo) GetByEmail(ctx context.Context, email string) (*domain.Admin, error) {
	row := r.db.pool.QueryRow(ctx, `SELECT `+adminCols+` FROM admins WHERE email=$1`, email)
	return scanAdmin(row)
}

func (r *AdminRepo) GetByID(ctx context.Context, id string) (*domain.Admin, error) {
	row := r.db.pool.QueryRow(ctx, `SELECT `+adminCols+` FROM admins WHERE id=$1`, id)
	return scanAdmin(row)
}

func (r *AdminRepo) ListAll(ctx context.Context) ([]domain.Admin, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+adminCols+` FROM admins ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Admin{}
	for rows.Next() {
		a, err := scanAdmin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (r *AdminRepo) Create(ctx context.Context, a domain.Admin) error {
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO admins (id, email, password_hash, name, role, requires_2fa)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		a.ID, a.Email, a.PasswordHash, a.Name, a.Role, a.RequiresTwoFA)
	return err
}

func (r *AdminRepo) UpdateRole(ctx context.Context, id, role string) error {
	tag, err := r.db.pool.Exec(ctx, `UPDATE admins SET role=$2 WHERE id=$1`, id, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *AdminRepo) UpdateRequires2FA(ctx context.Context, id string, requires bool) error {
	tag, err := r.db.pool.Exec(ctx, `UPDATE admins SET requires_2fa=$2 WHERE id=$1`, id, requires)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *AdminRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM admins WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func scanAdmin(row pgx.Row) (*domain.Admin, error) {
	var a domain.Admin
	err := row.Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Name, &a.Role, &a.RequiresTwoFA, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}
