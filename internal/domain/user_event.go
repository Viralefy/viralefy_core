package domain

import (
	"context"
	"time"
)

// UserEvent é um single behavior datapoint (pageview, click, modal_open,
// modal_close, checkout_start, checkout_complete, abandon, landing).
//
// visitor_id é client-supplied (não-confiável, sem PII). user_id é populado
// só quando há sessão autenticada — anônimos ficam só com visitor_id e a
// correlação é feita pós-login via INSERT trigger no service.
type UserEvent struct {
	ID         string         `json:"id"`
	VisitorID  string         `json:"visitor_id"`
	UserID     string         `json:"user_id,omitempty"`
	EventType  string         `json:"event_type"`
	Path       string         `json:"path,omitempty"`
	Referrer   string         `json:"referrer,omitempty"`
	Payload    map[string]any `json:"payload,omitempty"`
	UTM        map[string]any `json:"utm,omitempty"`
	IP         string         `json:"ip,omitempty"`
	UserAgent  string         `json:"user_agent,omitempty"`
	// AnalyticsConsent reflete o header `X-Analytics-Consent` no momento
	// do INSERT. Quando false, o repo NULLifica IP+UA — mas mantém o
	// próprio flag pra auditoria. Pointer pra distinguir "desconhecido"
	// (legacy/NULL) de "false" explícito.
	AnalyticsConsent *bool     `json:"analytics_consent,omitempty"`
	OccurredAt       time.Time `json:"occurred_at"`
}

// UserConsent é um registro append-only de decisão de consent. Grava o
// estado completo dos toggles + origem (qual botão o usuário clicou) +
// IP/UA — esses dois últimos são gravados SEMPRE porque a comprovação
// do consentimento (Art. 8 §6 LGPD) é base legal própria.
type UserConsent struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id,omitempty"`
	VisitorID   string    `json:"visitor_id,omitempty"`
	Version     int       `json:"version"`
	Necessary   bool      `json:"necessary"`
	Preferences bool      `json:"preferences"`
	Analytics   bool      `json:"analytics"`
	Marketing   bool      `json:"marketing"`
	Source      string    `json:"source"` // accept_all|essential_only|custom|reset
	IP          string    `json:"ip,omitempty"`
	UserAgent   string    `json:"user_agent,omitempty"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// UserConsentRepository — porta de saída pra persistência do audit log
// de consent. Append-only no app layer: nada de UPDATE/DELETE.
type UserConsentRepository interface {
	Record(ctx context.Context, c UserConsent) error
	ListByUser(ctx context.Context, userID string, limit int) ([]UserConsent, error)
}

// UserJourney é o agregado 1:1 por user. landing_* é first-touch
// (NÃO sobrescrito em updates). total_events/total_orders são bumpados pelo
// service em cada Record/order paid.
type UserJourney struct {
	UserID          string         `json:"user_id"`
	LandingPath     string         `json:"landing_path,omitempty"`
	LandingReferrer string         `json:"landing_referrer,omitempty"`
	LandingUTM      map[string]any `json:"landing_utm,omitempty"`
	FirstSeenAt     time.Time      `json:"first_seen_at"`
	LastSeenAt      time.Time      `json:"last_seen_at"`
	TotalEvents     int            `json:"total_events"`
	TotalOrders     int            `json:"total_orders"`
}

// VisitorSummary é o agregado para a listagem de visitors no painel admin.
// Não é uma tabela — calculamos em query (GROUP BY visitor_id em user_events).
// Quando o visitor converteu pra user, UserID/UserEmail/UserName aparecem.
type VisitorSummary struct {
	VisitorID    string    `json:"visitor_id"`
	UserID       *string   `json:"user_id,omitempty"`
	UserEmail    *string   `json:"user_email,omitempty"`
	UserName     *string   `json:"user_name,omitempty"`
	FirstSeenAt  time.Time `json:"first_seen_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
	TotalEvents  int       `json:"total_events"`
	LandingPath  *string   `json:"landing_path,omitempty"`
	LandingUTM   any       `json:"landing_utm,omitempty"`
	LastIP       *string   `json:"last_ip,omitempty"`
	LastUA       *string   `json:"last_user_agent,omitempty"`
}

// UserEventRepository é a porta de saída pra persistência de eventos +
// journey agregada.
type UserEventRepository interface {
	// Record grava um UserEvent append-only.
	Record(ctx context.Context, ev UserEvent) error
	// ListByVisitor devolve os eventos mais recentes do visitor (DESC).
	ListByVisitor(ctx context.Context, visitorID string, limit int) ([]UserEvent, error)
	// ListByUser devolve os eventos mais recentes do user autenticado (DESC).
	ListByUser(ctx context.Context, userID string, limit int) ([]UserEvent, error)
	// GetJourney devolve o agregado 1:1 do user. ErrNotFound quando inexistente.
	GetJourney(ctx context.Context, userID string) (*UserJourney, error)
	// UpsertJourney cria ou atualiza o agregado. landing_* só é gravado no
	// INSERT inicial (first-touch wins via COALESCE no UPDATE).
	UpsertJourney(ctx context.Context, j UserJourney) error
	// ListRecentVisitors devolve os visitors agrupados ordenados por
	// last_seen_at DESC. Usado no painel admin `/analytics/visitors`.
	ListRecentVisitors(ctx context.Context, limit, offset int) ([]VisitorSummary, error)
	// GetVisitorSummary devolve o agregado de UM visitor (ou ErrNotFound).
	GetVisitorSummary(ctx context.Context, visitorID string) (*VisitorSummary, error)
}
