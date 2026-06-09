package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type ProfileRepo struct{ db *DB }

func NewProfileRepo(db *DB) *ProfileRepo { return &ProfileRepo{db: db} }

const profileCols = `id, user_id, platform, handle, display_name, verified, created_at, updated_at`

func (r *ProfileRepo) Create(ctx context.Context, p domain.Profile) error {
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO profiles (id, user_id, platform, handle, display_name, verified)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		p.ID, p.UserID, p.Platform, p.Handle, p.DisplayName, p.Verified)
	return err
}

func (r *ProfileRepo) GetByID(ctx context.Context, id string) (*domain.Profile, error) {
	row := r.db.pool.QueryRow(ctx, `SELECT `+profileCols+` FROM profiles WHERE id=$1`, id)
	var p domain.Profile
	err := row.Scan(&p.ID, &p.UserID, &p.Platform, &p.Handle, &p.DisplayName, &p.Verified, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return &p, err
}

func (r *ProfileRepo) ListByUser(ctx context.Context, userID string) ([]domain.Profile, error) {
	return r.query(ctx, `SELECT `+profileCols+` FROM profiles WHERE user_id=$1 ORDER BY platform, handle`, userID)
}

func (r *ProfileRepo) ListByUserAndPlatform(ctx context.Context, userID string, platform domain.Platform) ([]domain.Profile, error) {
	return r.query(ctx, `SELECT `+profileCols+` FROM profiles WHERE user_id=$1 AND platform=$2 ORDER BY handle`, userID, string(platform))
}

func (r *ProfileRepo) query(ctx context.Context, sql string, args ...any) ([]domain.Profile, error) {
	rows, err := r.db.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []domain.Profile{}
	for rows.Next() {
		var p domain.Profile
		if err := rows.Scan(&p.ID, &p.UserID, &p.Platform, &p.Handle, &p.DisplayName, &p.Verified, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

func (r *ProfileRepo) Delete(ctx context.Context, id, userID string) error {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM profiles WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *ProfileRepo) SetVerified(ctx context.Context, id string, verified bool) error {
	tag, err := r.db.pool.Exec(ctx, `UPDATE profiles SET verified=$2, updated_at=NOW() WHERE id=$1`, id, verified)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
