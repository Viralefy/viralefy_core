package postgres

import (
	"context"
	"encoding/json"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

type AdminHoneypotRepo struct{ db *DB }

func NewAdminHoneypotRepo(db *DB) *AdminHoneypotRepo { return &AdminHoneypotRepo{db: db} }

func (r *AdminHoneypotRepo) Record(ctx context.Context, e domain.AdminHoneypotEntry) error {
	meta, _ := json.Marshal(e.Metadata)
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO admin_honeypot_log
			(id, actor_admin_id, target_admin_id, action, attempted_role, metadata)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
		e.ID, e.ActorAdminID, e.TargetAdminID, e.Action, e.AttemptedRole, meta,
	)
	return err
}

func (r *AdminHoneypotRepo) ActorHasShadowDeleted(ctx context.Context, actorAdminID, targetAdminID string) (bool, error) {
	var ok bool
	err := r.db.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM admin_honeypot_log
			WHERE actor_admin_id = $1 AND target_admin_id = $2 AND action = 'delete'
		)`, actorAdminID, targetAdminID).Scan(&ok)
	return ok, err
}

func (r *AdminHoneypotRepo) ActorShadowDeletedTargets(ctx context.Context, actorAdminID string) ([]string, error) {
	rows, err := r.db.pool.Query(ctx, `
		SELECT DISTINCT target_admin_id FROM admin_honeypot_log
		WHERE actor_admin_id = $1 AND action = 'delete'`, actorAdminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *AdminHoneypotRepo) ListAll(ctx context.Context, limit int) ([]domain.AdminHoneypotEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := r.db.pool.Query(ctx, `
		SELECT h.id, h.actor_admin_id, h.target_admin_id, h.action,
		       h.attempted_role, h.metadata, h.attempted_at,
		       a.email, a.name, t.email, t.name
		  FROM admin_honeypot_log h
		  LEFT JOIN admins a ON a.id = h.actor_admin_id
		  LEFT JOIN admins t ON t.id = h.target_admin_id
		 ORDER BY h.attempted_at DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AdminHoneypotEntry{}
	for rows.Next() {
		var e domain.AdminHoneypotEntry
		var meta []byte
		if err := rows.Scan(
			&e.ID, &e.ActorAdminID, &e.TargetAdminID, &e.Action,
			&e.AttemptedRole, &meta, &e.AttemptedAt,
			&e.ActorEmail, &e.ActorName, &e.TargetEmail, &e.TargetName,
		); err != nil {
			return nil, err
		}
		e.Metadata = map[string]any{}
		if len(meta) > 0 {
			_ = json.Unmarshal(meta, &e.Metadata)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
