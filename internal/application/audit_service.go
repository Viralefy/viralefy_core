package application

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
)

// AuditService grava no audit_log toda mutação relevante (plan/category/
// currency/gateway). Imutável: sem update nem delete. Linhas só crescem.
//
// Não bloqueia o caller — falha de gravação é logada mas não retornada.
// Isso evita que um erro no audit_log corrompa um update legítimo.
type AuditService struct {
	repo domain.AuditRepository
}

func NewAuditService(repo domain.AuditRepository) *AuditService {
	return &AuditService{repo: repo}
}

type AuditEntry struct {
	ActorType  string         // "admin" | "system"
	ActorID    string         // admin.id ou rótulo do sistema
	Action     string         // "create" | "update" | "delete"
	TargetType string         // "plan" | "category" | "currency" | "gateway"
	TargetID   string
	Before     any            // estado anterior (nil em create)
	After      any            // estado novo (nil em delete)
	Metadata   map[string]any // IP, user-agent, motivo
}

func (s *AuditService) Log(ctx context.Context, e AuditEntry) {
	beforeJSON, _ := json.Marshal(e.Before)
	afterJSON, _ := json.Marshal(e.After)
	metaJSON, _ := json.Marshal(e.Metadata)

	entry := domain.AuditEntry{
		ID:         uuid.New().String(),
		ActorType:  e.ActorType,
		ActorID:    e.ActorID,
		Action:     e.Action,
		TargetType: e.TargetType,
		TargetID:   e.TargetID,
		BeforeJSON: beforeJSON,
		AfterJSON:  afterJSON,
		Metadata:   metaJSON,
	}
	if err := s.repo.Insert(ctx, entry); err != nil {
		observability.FromContext(ctx).Warn("audit_log insert failed",
			"target_type", e.TargetType,
			"target_id", e.TargetID,
			"action", e.Action,
			"error", err.Error(),
		)
	}
}
