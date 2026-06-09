package domain

import (
	"context"
	"time"
)

type AuditEntry struct {
	ID         string    `json:"id"`
	ActorType  string    `json:"actor_type"`
	ActorID    string    `json:"actor_id"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	BeforeJSON []byte    `json:"before_data,omitempty"`
	AfterJSON  []byte    `json:"after_data,omitempty"`
	Metadata   []byte    `json:"metadata,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type AuditRepository interface {
	Insert(ctx context.Context, e AuditEntry) error
	List(ctx context.Context, targetType, targetID string, limit int) ([]AuditEntry, error)
}
