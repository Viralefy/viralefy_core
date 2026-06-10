package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
)

// UserConsentService — append-only do audit log de consentimento de cookies.
//
// Whitelist de `source` é estreita: apenas valores que o front pode mandar.
// Qualquer string fora desse set vira ErrInvalidInput (defesa-em-profundidade
// contra payload arbitrário via endpoint público).
var allowedConsentSources = map[string]struct{}{
	"accept_all":     {},
	"essential_only": {},
	"custom":         {},
	"reset":          {},
}

type UserConsentService struct {
	repo domain.UserConsentRepository
}

func NewUserConsentService(repo domain.UserConsentRepository) *UserConsentService {
	return &UserConsentService{repo: repo}
}

// ConsentInput é o shape recebido do handler. Source obrigatório; UserID
// opcional (visitante anônimo pode consentir antes de logar). IP+UA vêm
// do handler (clientIP + r.UserAgent()).
type ConsentInput struct {
	UserID      string
	VisitorID   string
	Version     int
	Necessary   bool
	Preferences bool
	Analytics   bool
	Marketing   bool
	Source      string
	IP          string
	UserAgent   string
	Timestamp   time.Time
}

// Record grava a decisão. Best-effort: falhas viram warn e retornam nil pro
// caller (o consent local no front já foi gravado — audit é redundante).
func (s *UserConsentService) Record(ctx context.Context, in ConsentInput) error {
	logger := observability.FromContext(ctx).With("svc", "user_consent")
	if _, ok := allowedConsentSources[in.Source]; !ok {
		logger.Warn("user_consent rejected: source not whitelisted", "source", in.Source)
		return domain.ErrInvalidInput
	}
	if in.Version <= 0 {
		logger.Warn("user_consent rejected: invalid version", "version", in.Version)
		return domain.ErrInvalidInput
	}
	ts := in.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	c := domain.UserConsent{
		ID:          uuid.New().String(),
		UserID:      in.UserID,
		VisitorID:   in.VisitorID,
		Version:     in.Version,
		Necessary:   in.Necessary,
		Preferences: in.Preferences,
		Analytics:   in.Analytics,
		Marketing:   in.Marketing,
		Source:      in.Source,
		IP:          in.IP,
		UserAgent:   in.UserAgent,
		RecordedAt:  ts,
	}
	if err := s.repo.Record(ctx, c); err != nil {
		logger.Warn("user_consent insert failed (best-effort)", "err", err.Error())
		return nil
	}
	return nil
}

// ListByUser — leitura autenticada do histórico de consents do user logado.
// Usado pra /v1/me/consent (GET) e pelo painel de privacidade.
func (s *UserConsentService) ListByUser(ctx context.Context, userID string, limit int) ([]domain.UserConsent, error) {
	return s.repo.ListByUser(ctx, userID, limit)
}
