package domain

import (
	"context"
	"time"
)

// AdminHoneypotEntry — registro de uma tentativa de um admin sobre o
// superadmin. Tabela admin_honeypot_log (migration 046).
//
// Também serve como flag de shadow-delete: existência de uma row com
// (actor, target, action='delete') esconde o target da lista do actor.
type AdminHoneypotEntry struct {
	ID             string         `json:"id"`
	ActorAdminID   string         `json:"actor_admin_id"`
	TargetAdminID  string         `json:"target_admin_id"`
	Action         string         `json:"action"`          // 'get' | 'update_role' | 'delete'
	AttemptedRole  *string        `json:"attempted_role,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	AttemptedAt    time.Time      `json:"attempted_at"`
	// Hidratados via JOIN com admins (read-side):
	ActorEmail  *string `json:"actor_email,omitempty"`
	ActorName   *string `json:"actor_name,omitempty"`
	TargetEmail *string `json:"target_email,omitempty"`
	TargetName  *string `json:"target_name,omitempty"`
}

const (
	HoneypotActionGet        = "get"
	HoneypotActionUpdateRole = "update_role"
	HoneypotActionDelete     = "delete"
)

type AdminHoneypotRepository interface {
	// Record grava uma nova tentativa (append-only).
	Record(ctx context.Context, e AdminHoneypotEntry) error
	// ActorHasShadowDeleted devolve true se existe uma row com
	// (actor_admin_id=actor, target_admin_id=target, action='delete').
	ActorHasShadowDeleted(ctx context.Context, actorAdminID, targetAdminID string) (bool, error)
	// ListAll devolve TODAS as tentativas (mais recentes primeiro) com
	// JOIN em admins pra hidratar email/name de actor e target.
	// Apenas superadmin pode ler.
	ListAll(ctx context.Context, limit int) ([]AdminHoneypotEntry, error)
	// ActorShadowDeletedTargets devolve a lista de target_admin_ids
	// shadow-deletados pelo actor — usado pra filtrar a lista de admins
	// que o actor vê.
	ActorShadowDeletedTargets(ctx context.Context, actorAdminID string) ([]string, error)
}
