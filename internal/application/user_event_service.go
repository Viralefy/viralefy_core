package application

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
)

// allowedEventTypes é a whitelist canônica de event_types aceitos pelo
// endpoint público /v1/track. Eventos fora desse set são rejeitados na
// borda — evita injeção arbitrária via client-supplied visitor_id.
var allowedEventTypes = map[string]struct{}{
	"pageview":          {},
	"click":             {},
	"modal_open":        {},
	"modal_close":       {},
	"checkout_start":    {},
	"checkout_complete": {},
	"abandon":           {},
	"landing":           {},
}

// IsAllowedEventType — utilitário pro handler validar antes de chamar
// RecordEvent. Exportado pra teste/observabilidade.
func IsAllowedEventType(t string) bool {
	_, ok := allowedEventTypes[t]
	return ok
}

// UserEventService — captura comportamento granular do usuário (anônimo
// via visitor_id ou autenticado via user_id). Best-effort: erros viram warn
// no logger, NÃO propagam pro caller. O propósito é não martelar o fluxo
// principal (checkout/pageview) quando o pipeline de tracking falha.
type UserEventService struct {
	repo domain.UserEventRepository
}

func NewUserEventService(repo domain.UserEventRepository) *UserEventService {
	return &UserEventService{repo: repo}
}

// EventInput é o shape recebido do handler. Não obriga ID — o service
// gera UUID se vazio.
type EventInput struct {
	ID        string
	VisitorID string
	UserID    string
	EventType string
	Path      string
	Referrer  string
	Payload   map[string]any
	UTM       map[string]any
	IP        string
	UserAgent string
}

// RecordEvent grava o evento + bumpa user_journeys.
//
// Fluxo:
//  1. Valida visitor_id + event_type (whitelist).
//  2. INSERT em user_events (append-only).
//  3. Se user_id != "", UPSERT em user_journeys (bumpa last_seen_at +
//     total_events). Quando event_type == "landing" e é a primeira gravação,
//     o INSERT seta landing_path/referrer/utm.
//
// Erros viram warn — o caller (handler /v1/track) responde 204 mesmo em
// falha porque tracking não-crítico não deve quebrar UX.
func (s *UserEventService) RecordEvent(ctx context.Context, in EventInput) error {
	logger := observability.FromContext(ctx).With("svc", "user_event")
	if in.VisitorID == "" {
		logger.Warn("user_event missing visitor_id", "event_type", in.EventType)
		return domain.ErrInvalidInput
	}
	if !IsAllowedEventType(in.EventType) {
		logger.Warn("user_event rejected: event_type not whitelisted", "event_type", in.EventType)
		return domain.ErrInvalidInput
	}
	id := in.ID
	if id == "" {
		id = uuid.New().String()
	}
	ev := domain.UserEvent{
		ID:        id,
		VisitorID: in.VisitorID,
		UserID:    in.UserID,
		EventType: in.EventType,
		Path:      in.Path,
		Referrer:  in.Referrer,
		Payload:   in.Payload,
		UTM:       in.UTM,
		IP:        in.IP,
		UserAgent: in.UserAgent,
	}
	if err := s.repo.Record(ctx, ev); err != nil {
		logger.Warn("user_event insert failed (best-effort)", "err", err.Error())
		return nil
	}
	if in.UserID != "" {
		j := domain.UserJourney{
			UserID: in.UserID,
		}
		// landing_* só é gravado no INSERT inicial (ON CONFLICT mantém o
		// existente). Mas só faz sentido populá-los quando o evento atual
		// é "landing" — caso contrário deixa vazio e o ON CONFLICT no
		// repo decide o caminho (UPDATE puro).
		if in.EventType == "landing" {
			j.LandingPath = in.Path
			j.LandingReferrer = in.Referrer
			j.LandingUTM = in.UTM
		}
		if err := s.repo.UpsertJourney(ctx, j); err != nil {
			logger.Warn("user_journey upsert failed (best-effort)", "err", err.Error())
		}
	}
	return nil
}

// LandingFromTracking extrai landing_url/referrer/utm.* do objeto `tracking`
// que o front manda no register/checkout. Conveniência pra outros services
// (auth_service/checkout_service) sintetizarem um EventInput type=landing
// na primeira gravação do user.
//
// Retorna (landingPath, referrer, utmMap). utmMap fica nil quando não há
// nenhum utm_* preenchido (evita gravar {} JSON).
func LandingFromTracking(tracking map[string]any) (landingPath, referrer string, utm map[string]any) {
	if tracking == nil {
		return "", "", nil
	}
	if v, ok := tracking["landing_url"].(string); ok {
		landingPath = v
	}
	if v, ok := tracking["referrer"].(string); ok {
		referrer = v
	}
	utm = map[string]any{}
	for k, v := range tracking {
		if strings.HasPrefix(k, "utm_") {
			if s, ok := v.(string); ok && s != "" {
				utm[k] = s
			}
		}
	}
	if len(utm) == 0 {
		utm = nil
	}
	return landingPath, referrer, utm
}

// ListByUser devolve os últimos N eventos do user — usado por /v1/me/journey
// pra montar a timeline mostrada no painel.
func (s *UserEventService) ListByUser(ctx context.Context, userID string, limit int) ([]domain.UserEvent, error) {
	return s.repo.ListByUser(ctx, userID, limit)
}

// GetJourney devolve o agregado 1:1. Quando ainda não existe, devolve um
// zero-value (em vez de ErrNotFound) — o front renderiza "Sem dados ainda"
// sem precisar tratar 404.
func (s *UserEventService) GetJourney(ctx context.Context, userID string) (*domain.UserJourney, error) {
	j, err := s.repo.GetJourney(ctx, userID)
	if err == domain.ErrNotFound {
		return &domain.UserJourney{UserID: userID}, nil
	}
	return j, err
}
