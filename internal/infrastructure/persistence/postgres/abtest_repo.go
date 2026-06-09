package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// ABTestRepo persiste experimentos, assignments e events.
type ABTestRepo struct {
	db *DB
}

func NewABTestRepo(db *DB) *ABTestRepo {
	return &ABTestRepo{db: db}
}

// --- experiments ---

func scanExperiment(row pgx.Row) (*domain.ABExperiment, error) {
	var e domain.ABExperiment
	var variantsJSON []byte
	if err := row.Scan(&e.Key, &e.Description, &variantsJSON, &e.Active, &e.CreatedAt); err != nil {
		return nil, err
	}
	if len(variantsJSON) > 0 {
		if err := json.Unmarshal(variantsJSON, &e.Variants); err != nil {
			return nil, err
		}
	}
	return &e, nil
}

func (r *ABTestRepo) GetExperiment(ctx context.Context, key string) (*domain.ABExperiment, error) {
	row := r.db.pool.QueryRow(ctx,
		`SELECT key, description, variants, active, created_at FROM ab_experiments WHERE key = $1`,
		key,
	)
	e, err := scanExperiment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return e, err
}

func (r *ABTestRepo) ListExperiments(ctx context.Context) ([]domain.ABExperiment, error) {
	rows, err := r.db.pool.Query(ctx,
		`SELECT key, description, variants, active, created_at FROM ab_experiments ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ABExperiment{}
	for rows.Next() {
		e, err := scanExperiment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (r *ABTestRepo) CreateExperiment(ctx context.Context, e domain.ABExperiment) error {
	variantsJSON, err := json.Marshal(e.Variants)
	if err != nil {
		return err
	}
	_, err = r.db.pool.Exec(ctx, `
		INSERT INTO ab_experiments (key, description, variants, active)
		VALUES ($1, $2, $3::jsonb, $4)`,
		e.Key, e.Description, string(variantsJSON), e.Active,
	)
	return err
}

func (r *ABTestRepo) UpdateExperiment(ctx context.Context, e domain.ABExperiment) error {
	variantsJSON, err := json.Marshal(e.Variants)
	if err != nil {
		return err
	}
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE ab_experiments
		   SET description = $2,
		       variants    = $3::jsonb,
		       active      = $4
		 WHERE key = $1`,
		e.Key, e.Description, string(variantsJSON), e.Active,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// --- assignments ---

func (r *ABTestRepo) GetAssignment(ctx context.Context, visitorID, experimentKey string) (*domain.ABAssignment, error) {
	var a domain.ABAssignment
	err := r.db.pool.QueryRow(ctx, `
		SELECT visitor_id, experiment_key, variant, assigned_at
		  FROM ab_assignments
		 WHERE visitor_id = $1 AND experiment_key = $2`,
		visitorID, experimentKey,
	).Scan(&a.VisitorID, &a.ExperimentKey, &a.Variant, &a.AssignedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *ABTestRepo) CreateAssignment(ctx context.Context, a domain.ABAssignment) error {
	// ON CONFLICT: 2 requests do mesmo visitor caindo na decisão ao mesmo
	// tempo não causam erro — vence o primeiro insert (que tem o variant
	// determinístico hash-based; ambos os races chegariam ao mesmo valor).
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO ab_assignments (visitor_id, experiment_key, variant)
		VALUES ($1, $2, $3)
		ON CONFLICT (visitor_id, experiment_key) DO NOTHING`,
		a.VisitorID, a.ExperimentKey, a.Variant,
	)
	return err
}

// --- events ---

func (r *ABTestRepo) CreateEvent(ctx context.Context, ev domain.ABEvent) error {
	var payloadJSON []byte
	if ev.Payload != nil {
		b, err := json.Marshal(ev.Payload)
		if err != nil {
			return err
		}
		payloadJSON = b
	}
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO ab_events (id, visitor_id, experiment_key, variant, event_name, payload)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
		ev.ID, ev.VisitorID, ev.ExperimentKey, ev.Variant, ev.EventName, nullableJSON(payloadJSON),
	)
	return err
}

// nullableJSON devolve string vazia quando payloadJSON é nil — o driver
// passa NULL. Evita gravar literal `null` quando o caller não mandou nada.
func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
