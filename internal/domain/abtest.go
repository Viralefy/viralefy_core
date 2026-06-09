package domain

import (
	"context"
	"errors"
	"time"
)

// ABExperiment é a definição de um experimento A/B.
// Variants é um map variant→peso (relativo, não precisa somar 100). O
// serviço normaliza no sample.
type ABExperiment struct {
	Key         string         `json:"key"`
	Description string         `json:"description"`
	Variants    map[string]int `json:"variants"`
	Active      bool           `json:"active"`
	CreatedAt   time.Time      `json:"created_at"`
}

// ABAssignment é o registro sticky: cada visitor cai em UMA variant por
// experimento, e essa decisão é persistida pra reprodutibilidade entre
// requests/dispositivos (mesmo visitor_id).
type ABAssignment struct {
	VisitorID     string    `json:"visitor_id"`
	ExperimentKey string    `json:"experiment_key"`
	Variant       string    `json:"variant"`
	AssignedAt    time.Time `json:"assigned_at"`
}

// ABEvent é uma observação append-only.
// EventName canônicos:
//   "exposure"   — visitor viu a variant (instrumentado no componente)
//   "conversion" — visitor cumpriu o objetivo (checkout, signup, etc.)
//   custom       — qualquer string livre que o produto queira trackear.
type ABEvent struct {
	ID            string         `json:"id"`
	VisitorID     string         `json:"visitor_id"`
	ExperimentKey string         `json:"experiment_key"`
	Variant       string         `json:"variant"`
	EventName     string         `json:"event_name"`
	Payload       map[string]any `json:"payload,omitempty"`
	OccurredAt    time.Time      `json:"occurred_at"`
}

// ErrExperimentInactive é devolvido por GetAssignment quando o experimento
// existe mas está desligado. Front trata fazendo fallback pro "control".
var ErrExperimentInactive = errors.New("experiment inactive")

// ABTestRepository — porta de saída pra persistência.
type ABTestRepository interface {
	// Experiments
	GetExperiment(ctx context.Context, key string) (*ABExperiment, error)
	ListExperiments(ctx context.Context) ([]ABExperiment, error)
	CreateExperiment(ctx context.Context, e ABExperiment) error
	UpdateExperiment(ctx context.Context, e ABExperiment) error

	// Assignments
	GetAssignment(ctx context.Context, visitorID, experimentKey string) (*ABAssignment, error)
	CreateAssignment(ctx context.Context, a ABAssignment) error

	// Events
	CreateEvent(ctx context.Context, ev ABEvent) error
}
