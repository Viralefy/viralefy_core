package postgres

import (
	"context"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

type RoleRepo struct{ db *DB }

func NewRoleRepo(db *DB) *RoleRepo { return &RoleRepo{db: db} }

func (r *RoleRepo) GetPermissions(ctx context.Context, roleCode string) ([]string, error) {
	rows, err := r.db.pool.Query(ctx, `
		SELECT permission FROM role_permissions WHERE role_code = $1 ORDER BY permission`, roleCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	perms := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		perms = append(perms, p)
	}
	return perms, rows.Err()
}

func (r *RoleRepo) List(ctx context.Context) ([]domain.Role, error) {
	rows, err := r.db.pool.Query(ctx, `
		SELECT r.code, r.label, COALESCE(array_agg(rp.permission) FILTER (WHERE rp.permission IS NOT NULL), '{}')
		FROM roles r LEFT JOIN role_permissions rp ON rp.role_code = r.code
		GROUP BY r.code, r.label ORDER BY r.code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []domain.Role{}
	for rows.Next() {
		var role domain.Role
		if err := rows.Scan(&role.Code, &role.Label, &role.Permissions); err != nil {
			return nil, err
		}
		list = append(list, role)
	}
	return list, rows.Err()
}
