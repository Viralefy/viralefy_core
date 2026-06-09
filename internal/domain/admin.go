package domain

import (
	"context"
	"time"
)

type Admin struct {
	ID            string
	Email         string
	PasswordHash  string
	Name          string
	Role          string
	// RequiresTwoFA — controla se o login bloqueia em partial_token quando
	// 2FA service está plugado. Migration 036 default TRUE pra todos.
	// Superadmin pode desabilitar via /admins/[id] em casos excepcionais
	// (recovery, account compartilhada de servidor — não recomendado).
	RequiresTwoFA bool
	CreatedAt     time.Time
}

type AdminRepository interface {
	GetByEmail(ctx context.Context, email string) (*Admin, error)
	GetByID(ctx context.Context, id string) (*Admin, error)
	ListAll(ctx context.Context) ([]Admin, error)
	Create(ctx context.Context, a Admin) error
	UpdateRole(ctx context.Context, id, role string) error
	UpdateRequires2FA(ctx context.Context, id string, requires bool) error
	Delete(ctx context.Context, id string) error
}
