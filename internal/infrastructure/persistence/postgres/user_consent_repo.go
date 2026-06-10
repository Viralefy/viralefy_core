package postgres

import (
	"context"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// UserConsentRepo — audit log de decisões de consent (LGPD Art. 8 §6).
// Append-only. IP/UA são gravados sempre porque a base legal aqui é a
// própria comprovação do consentimento.
type UserConsentRepo struct {
	db *DB
}

func NewUserConsentRepo(db *DB) *UserConsentRepo {
	return &UserConsentRepo{db: db}
}

// Record grava uma decisão. ID/RecordedAt vêm pré-preenchidos pelo
// service. user_id/visitor_id podem ser vazios (anônimo sem visitor_id
// ainda — raro mas possível em edge browsers); pgx nullifica.
func (r *UserConsentRepo) Record(ctx context.Context, c domain.UserConsent) error {
	var userIDArg, visitorIDArg, ipArg, uaArg any
	if c.UserID != "" {
		userIDArg = c.UserID
	}
	if c.VisitorID != "" {
		visitorIDArg = c.VisitorID
	}
	if c.IP != "" {
		ipArg = c.IP
	}
	if c.UserAgent != "" {
		uaArg = c.UserAgent
	}
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO user_consent_log
			(id, user_id, visitor_id, version, necessary, preferences,
			 analytics, marketing, source, ip, user_agent, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		c.ID, userIDArg, visitorIDArg, c.Version, c.Necessary, c.Preferences,
		c.Analytics, c.Marketing, c.Source, ipArg, uaArg, c.RecordedAt,
	)
	return err
}

func (r *UserConsentRepo) ListByUser(ctx context.Context, userID string, limit int) ([]domain.UserConsent, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := r.db.pool.Query(ctx, `
		SELECT id, user_id, visitor_id, version, necessary, preferences,
		       analytics, marketing, source, ip, user_agent, recorded_at
		  FROM user_consent_log
		 WHERE user_id = $1
		 ORDER BY recorded_at DESC
		 LIMIT $2`,
		userID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.UserConsent{}
	for rows.Next() {
		var c domain.UserConsent
		var userIDPtr, visitorIDPtr, ipPtr, uaPtr *string
		if err := rows.Scan(
			&c.ID, &userIDPtr, &visitorIDPtr, &c.Version, &c.Necessary,
			&c.Preferences, &c.Analytics, &c.Marketing, &c.Source,
			&ipPtr, &uaPtr, &c.RecordedAt,
		); err != nil {
			return nil, err
		}
		if userIDPtr != nil {
			c.UserID = *userIDPtr
		}
		if visitorIDPtr != nil {
			c.VisitorID = *visitorIDPtr
		}
		if ipPtr != nil {
			c.IP = *ipPtr
		}
		if uaPtr != nil {
			c.UserAgent = *uaPtr
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
