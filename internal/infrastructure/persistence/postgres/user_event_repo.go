package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// UserEventRepo persiste user_events (append-only) + user_journeys
// (agregado 1:1 por user). visitor_id é client-supplied; user_id é nullable.
type UserEventRepo struct {
	db *DB
}

func NewUserEventRepo(db *DB) *UserEventRepo {
	return &UserEventRepo{db: db}
}

// Record grava um evento granular. payload/utm são JSONB nullable.
func (r *UserEventRepo) Record(ctx context.Context, ev domain.UserEvent) error {
	var payloadJSON, utmJSON []byte
	if ev.Payload != nil {
		b, err := json.Marshal(ev.Payload)
		if err != nil {
			return err
		}
		payloadJSON = b
	}
	if ev.UTM != nil {
		b, err := json.Marshal(ev.UTM)
		if err != nil {
			return err
		}
		utmJSON = b
	}
	// user_id é nullable — pgx serializa "" como '' (não NULL); usamos *string.
	var userIDArg any
	if ev.UserID != "" {
		userIDArg = ev.UserID
	} else {
		userIDArg = nil
	}
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO user_events
			(id, visitor_id, user_id, event_type, path, referrer, payload, utm, ip, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		ev.ID, ev.VisitorID, userIDArg, ev.EventType, ev.Path, ev.Referrer,
		nullableJSON(payloadJSON), nullableJSON(utmJSON), ev.IP, ev.UserAgent,
	)
	return err
}

func scanUserEvent(row pgx.Row) (*domain.UserEvent, error) {
	var ev domain.UserEvent
	var userID *string
	var path, referrer, ip, ua *string
	var payloadJSON, utmJSON []byte
	if err := row.Scan(
		&ev.ID, &ev.VisitorID, &userID, &ev.EventType,
		&path, &referrer, &payloadJSON, &utmJSON, &ip, &ua, &ev.OccurredAt,
	); err != nil {
		return nil, err
	}
	if userID != nil {
		ev.UserID = *userID
	}
	if path != nil {
		ev.Path = *path
	}
	if referrer != nil {
		ev.Referrer = *referrer
	}
	if ip != nil {
		ev.IP = *ip
	}
	if ua != nil {
		ev.UserAgent = *ua
	}
	if len(payloadJSON) > 0 {
		_ = json.Unmarshal(payloadJSON, &ev.Payload)
	}
	if len(utmJSON) > 0 {
		_ = json.Unmarshal(utmJSON, &ev.UTM)
	}
	return &ev, nil
}

const userEventSelect = `
	SELECT id, visitor_id, user_id, event_type, path, referrer,
	       payload, utm, ip, user_agent, occurred_at
	  FROM user_events`

func (r *UserEventRepo) ListByVisitor(ctx context.Context, visitorID string, limit int) ([]domain.UserEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := r.db.pool.Query(ctx,
		userEventSelect+` WHERE visitor_id = $1 ORDER BY occurred_at DESC LIMIT $2`,
		visitorID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.UserEvent{}
	for rows.Next() {
		ev, err := scanUserEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ev)
	}
	return out, rows.Err()
}

func (r *UserEventRepo) ListByUser(ctx context.Context, userID string, limit int) ([]domain.UserEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := r.db.pool.Query(ctx,
		userEventSelect+` WHERE user_id = $1 ORDER BY occurred_at DESC LIMIT $2`,
		userID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.UserEvent{}
	for rows.Next() {
		ev, err := scanUserEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ev)
	}
	return out, rows.Err()
}

func (r *UserEventRepo) GetJourney(ctx context.Context, userID string) (*domain.UserJourney, error) {
	var j domain.UserJourney
	var landingPath, landingReferrer *string
	var landingUTMJSON []byte
	err := r.db.pool.QueryRow(ctx, `
		SELECT user_id, landing_path, landing_referrer, landing_utm,
		       first_seen_at, last_seen_at, total_events, total_orders
		  FROM user_journeys
		 WHERE user_id = $1`,
		userID,
	).Scan(
		&j.UserID, &landingPath, &landingReferrer, &landingUTMJSON,
		&j.FirstSeenAt, &j.LastSeenAt, &j.TotalEvents, &j.TotalOrders,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if landingPath != nil {
		j.LandingPath = *landingPath
	}
	if landingReferrer != nil {
		j.LandingReferrer = *landingReferrer
	}
	if len(landingUTMJSON) > 0 {
		_ = json.Unmarshal(landingUTMJSON, &j.LandingUTM)
	}
	return &j, nil
}

// UpsertJourney cria ou atualiza o agregado.
//
// Invariante first-touch wins: landing_* só é gravado uma vez por user.
// Não confiamos no SERVICE pra garantir ordenação porque o primeiro evento
// observado pode não ser type='landing' (ex.: signup pré-pageview). COALESCE
// no UPDATE garante: existente ganha. Quando existente é NULL (caso comum
// se o primeiro evento foi 'click' antes do pageview chegar), o EXCLUDED
// (landing event posterior) preenche.
func (r *UserEventRepo) UpsertJourney(ctx context.Context, j domain.UserJourney) error {
	var landingUTMJSON []byte
	if j.LandingUTM != nil {
		b, err := json.Marshal(j.LandingUTM)
		if err != nil {
			return err
		}
		landingUTMJSON = b
	}
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO user_journeys
			(user_id, landing_path, landing_referrer, landing_utm,
			 first_seen_at, last_seen_at, total_events, total_orders)
		VALUES ($1, NULLIF($2,''), NULLIF($3,''), $4::jsonb,
		        NOW(), NOW(), 1, 0)
		ON CONFLICT (user_id) DO UPDATE
		   SET last_seen_at      = NOW(),
		       total_events      = user_journeys.total_events + 1,
		       landing_path      = COALESCE(user_journeys.landing_path, EXCLUDED.landing_path),
		       landing_referrer  = COALESCE(user_journeys.landing_referrer, EXCLUDED.landing_referrer),
		       landing_utm       = COALESCE(user_journeys.landing_utm, EXCLUDED.landing_utm)`,
		j.UserID, j.LandingPath, j.LandingReferrer, nullableJSON(landingUTMJSON),
	)
	return err
}
