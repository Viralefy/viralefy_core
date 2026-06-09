package postgres

import (
	"context"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

type CategoryRepo struct{ db *DB }

func NewCategoryRepo(db *DB) *CategoryRepo { return &CategoryRepo{db: db} }

func (r *CategoryRepo) ListActive(ctx context.Context) ([]domain.Category, error) {
	rows, err := r.db.pool.Query(ctx, `
		SELECT code, label, sort_order, active FROM categories WHERE active = true ORDER BY sort_order ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []domain.Category{}
	for rows.Next() {
		var c domain.Category
		if err := rows.Scan(&c.Code, &c.Label, &c.SortOrder, &c.Active); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}
