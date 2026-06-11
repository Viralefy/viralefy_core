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
//
// PII gate (LGPD Art. 8 §3): quando ev.AnalyticsConsent é não-nil E false,
// IP+UA são gravados como NULL — preservamos contagem de eventos pra
// métricas de produto, mas sem dado pessoal. Quando o flag é nil (legacy
// path, ou caller que esqueceu de setar), o comportamento padrão é
// CONSERVADOR: também NULLify IP/UA. Só grava IP/UA quando explicitamente
// true.
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
	// Privacy-by-default: IP/UA só vão pro DB se consent EXPLICITAMENTE true.
	consentOK := ev.AnalyticsConsent != nil && *ev.AnalyticsConsent
	var ipArg, uaArg any
	if consentOK {
		ipArg = ev.IP
		uaArg = ev.UserAgent
	} else {
		ipArg = nil
		uaArg = nil
	}
	// analytics_consent é nullable BOOLEAN. NULL = pre-feature legacy;
	// false = consent negado; true = consent dado.
	var consentArg any
	if ev.AnalyticsConsent != nil {
		consentArg = *ev.AnalyticsConsent
	} else {
		consentArg = nil
	}
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO user_events
			(id, visitor_id, user_id, event_type, path, referrer, payload, utm, ip, user_agent, analytics_consent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		ev.ID, ev.VisitorID, userIDArg, ev.EventType, ev.Path, ev.Referrer,
		nullableJSON(payloadJSON), nullableJSON(utmJSON), ipArg, uaArg, consentArg,
	)
	return err
}

func scanUserEvent(row pgx.Row) (*domain.UserEvent, error) {
	var ev domain.UserEvent
	var userID *string
	var path, referrer, ip, ua *string
	var consent *bool
	var payloadJSON, utmJSON []byte
	if err := row.Scan(
		&ev.ID, &ev.VisitorID, &userID, &ev.EventType,
		&path, &referrer, &payloadJSON, &utmJSON, &ip, &ua, &consent, &ev.OccurredAt,
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
	ev.AnalyticsConsent = consent
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
	       payload, utm, ip, user_agent, analytics_consent, occurred_at
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

// ListRecentVisitors agrupa user_events por visitor_id e ordena por
// last_seen_at DESC. Junta com users por user_id (LEFT JOIN — visitor anônimo
// fica com user_email/user_name NULL). Landing path/UTM vêm do primeiro
// evento gravado pra esse visitor (subquery por ordem ASC).
//
// Paginação: LIMIT/OFFSET simples — pra catálogos grandes vamos precisar
// cursor; por enquanto serve.
func (r *UserEventRepo) ListRecentVisitors(ctx context.Context, limit, offset int) ([]domain.VisitorSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.db.pool.Query(ctx, `
		WITH agg AS (
			SELECT
				visitor_id,
				MAX(user_id)            AS user_id,
				MIN(occurred_at)        AS first_seen_at,
				MAX(occurred_at)        AS last_seen_at,
				COUNT(*)                AS total_events
			FROM user_events
			GROUP BY visitor_id
		),
		landing AS (
			SELECT DISTINCT ON (visitor_id)
				visitor_id, path AS landing_path, utm AS landing_utm
			FROM user_events
			ORDER BY visitor_id, occurred_at ASC
		),
		lastctx AS (
			SELECT DISTINCT ON (visitor_id)
				visitor_id, ip AS last_ip, user_agent AS last_ua
			FROM user_events
			ORDER BY visitor_id, occurred_at DESC
		)
		SELECT a.visitor_id, a.user_id, u.email, u.name,
		       a.first_seen_at, a.last_seen_at, a.total_events,
		       l.landing_path, l.landing_utm,
		       c.last_ip, c.last_ua
		  FROM agg a
		  LEFT JOIN users   u ON u.id = a.user_id
		  LEFT JOIN landing l ON l.visitor_id = a.visitor_id
		  LEFT JOIN lastctx c ON c.visitor_id = a.visitor_id
		 ORDER BY a.last_seen_at DESC
		 LIMIT $1 OFFSET $2`,
		limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.VisitorSummary{}
	for rows.Next() {
		v, err := scanVisitorSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// GetVisitorSummary devolve o agregado de UM visitor. ErrNotFound quando o
// visitor não tem nenhum evento.
func (r *UserEventRepo) GetVisitorSummary(ctx context.Context, visitorID string) (*domain.VisitorSummary, error) {
	row := r.db.pool.QueryRow(ctx, `
		WITH agg AS (
			SELECT
				visitor_id,
				MAX(user_id)            AS user_id,
				MIN(occurred_at)        AS first_seen_at,
				MAX(occurred_at)        AS last_seen_at,
				COUNT(*)                AS total_events
			FROM user_events
			WHERE visitor_id = $1
			GROUP BY visitor_id
		),
		landing AS (
			SELECT path AS landing_path, utm AS landing_utm
			FROM user_events
			WHERE visitor_id = $1
			ORDER BY occurred_at ASC
			LIMIT 1
		),
		lastctx AS (
			SELECT ip AS last_ip, user_agent AS last_ua
			FROM user_events
			WHERE visitor_id = $1
			ORDER BY occurred_at DESC
			LIMIT 1
		)
		SELECT a.visitor_id, a.user_id, u.email, u.name,
		       a.first_seen_at, a.last_seen_at, a.total_events,
		       l.landing_path, l.landing_utm,
		       c.last_ip, c.last_ua
		  FROM agg a
		  LEFT JOIN users   u ON u.id = a.user_id
		  CROSS JOIN landing l
		  CROSS JOIN lastctx c`,
		visitorID,
	)
	v, err := scanVisitorSummary(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

func scanVisitorSummary(row pgx.Row) (*domain.VisitorSummary, error) {
	var v domain.VisitorSummary
	var landingUTMJSON []byte
	err := row.Scan(
		&v.VisitorID, &v.UserID, &v.UserEmail, &v.UserName,
		&v.FirstSeenAt, &v.LastSeenAt, &v.TotalEvents,
		&v.LandingPath, &landingUTMJSON,
		&v.LastIP, &v.LastUA,
	)
	if err != nil {
		return nil, err
	}
	if len(landingUTMJSON) > 0 {
		var utm map[string]any
		if jerr := json.Unmarshal(landingUTMJSON, &utm); jerr == nil {
			v.LandingUTM = utm
		}
	}
	return &v, nil
}
