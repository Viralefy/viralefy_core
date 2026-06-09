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
	OccurredAt time.Time      `json:"occurred_at"`
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
}
