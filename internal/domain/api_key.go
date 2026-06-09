package domain

import (
	"context"
	"time"
)

// APIKey é uma credencial B2B emitida a um usuário (owner) para chamar
// endpoints públicos read-only em /v2 via header X-API-Key. A key plain
// nunca é persistida — apenas key_hash (SHA-256).
//
// V2 roadmap: api_key_usage para rate-limit/billing per-key. Aqui só
// scaffold de auth + revoke + audit (last_used_at).
type APIKey struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	OwnerUserID string     `json:"owner_user_id"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

// APIKeyRepository é a porta de saída para persistência.
//
// Create grava a row com key_hash já calculado pelo service.
// GetByHash retorna apenas keys ativas (revoked_at IS NULL) — é o caminho
// quente do middleware.
// MarkUsed atualiza last_used_at best-effort (chamada async pelo service).
type APIKeyRepository interface {
	Create(ctx context.Context, k APIKey, keyHash string) error
	GetByHash(ctx context.Context, keyHash string) (*APIKey, error)
	ListByUser(ctx context.Context, userID string) ([]APIKey, error)
	Revoke(ctx context.Context, userID, keyID string) error
	MarkUsed(ctx context.Context, keyID string) error
}
