package postgres

import (
	"context"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

type AuditRepo struct{ db *DB }

func NewAuditRepo(db *DB) *AuditRepo { return &AuditRepo{db: db} }

func (r *AuditRepo) Insert(ctx context.Context, e domain.AuditEntry) error {
	// Garantia: nunca passamos NULL pra colunas JSONB que default ao '{}'.
	if len(e.Metadata) == 0 {
		e.Metadata = []byte("{}")
	}
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO audit_log (id, actor_type, actor_id, action,
		                       target_type, target_id, before_data, after_data, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)`,
		e.ID, e.ActorType, e.ActorID, e.Action,
		e.TargetType, e.TargetID, nullableBytes(e.BeforeJSON), nullableBytes(e.AfterJSON), e.Metadata)
	return err
}

func (r *AuditRepo) List(ctx context.Context, targetType, targetID string, limit int) ([]domain.AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.pool.Query(ctx, `
		SELECT id, actor_type, actor_id, action, target_type, target_id,
		       before_data, after_data, metadata, created_at
		  FROM audit_log
		 WHERE target_type = $1 AND target_id = $2
		 ORDER BY created_at DESC LIMIT $3`, targetType, targetID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AuditEntry{}
	for rows.Next() {
		var e domain.AuditEntry
		if err := rows.Scan(&e.ID, &e.ActorType, &e.ActorID, &e.Action,
			&e.TargetType, &e.TargetID, &e.BeforeJSON, &e.AfterJSON, &e.Metadata, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// nullableBytes mapeia slice vazio para nil (NULL no SQL) — útil para
// columns JSONB nulláveis (before em create / after em delete).
func nullableBytes(b []byte) any {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return b
}
